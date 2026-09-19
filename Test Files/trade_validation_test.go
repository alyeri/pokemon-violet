package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testTradeValidationService(t *testing.T) *tradeValidationService {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, tradeSignatureSize*8)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTradeValidationService(key)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testTradeRequest(entries uint16) ([]byte, []byte) {
	prefix := []byte("violet-test-client\x00")
	header := []byte{0, 0, 0, 1, 0, 1}
	body := append(append([]byte(nil), prefix...), header...)
	count := []byte{0, 0}
	binary.BigEndian.PutUint16(count, entries)
	body = append(body, count...)
	party := make([]byte, int(entries)*tradePartyEntrySize)
	for index := range party {
		party[index] = byte(index*31 + 7)
	}
	return append(body, party...), party
}

func performTradeRequest(handler http.HandlerFunc, method string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.invalid/", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func TestTradeValidationKeyRefreshAndAcceptedSignature(t *testing.T) {
	service := testTradeValidationService(t)
	body, party := testTradeRequest(2)

	refresh := performTradeRequest(service.serveValidate, http.MethodPost, body)
	if refresh.Code != http.StatusOK || !bytes.Equal(refresh.Body.Bytes(), []byte{2}) {
		t.Fatalf("first validate = status %d body %x, want 200/02", refresh.Code, refresh.Body.Bytes())
	}

	publicKeyResponse := performTradeRequest(service.servePublicKey, http.MethodPost, []byte("violet-test-client\x00"))
	keyBody := publicKeyResponse.Body.Bytes()
	if publicKeyResponse.Code != 200 || len(keyBody) < 3 || keyBody[len(keyBody)-1] != 0 {
		t.Fatal("missing successful NUL-terminated public key")
	}
	version := binary.BigEndian.Uint16(keyBody[:2])
	if version == 0 || version != service.version {
		t.Fatal("invalid public key version")
	}
	encoded := keyBody[2 : len(keyBody)-1]
	der, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key has type %T", parsed)
	}

	// Emulate native 0x16d0b00 -> 0x1d5f91c -> request serializer:
	// receiving a key stores its version, echoed in the next POST.
	binary.BigEndian.PutUint16(body[bytes.IndexByte(body, 0)+1:], version)
	retry := httptest.NewRequest(http.MethodPost, "/v1/validate", bytes.NewReader(body))
	retry.RemoteAddr = "127.0.0.1:54321" // a fresh connection without auth headers
	accepted := httptest.NewRecorder()
	service.serveValidate(accepted, retry)
	response := accepted.Body.Bytes()
	if accepted.Code != http.StatusOK || len(response) != 7+tradeSignatureSize {
		t.Fatalf("accepted validate = status %d length %d", accepted.Code, len(response))
	}
	if !bytes.Equal(response[:7], make([]byte, 7)) {
		t.Fatalf("accepted response header = %x", response[:7])
	}
	message := append(append([]byte(nil), party...), 0, 1, 0, 1)
	digest := sha256.Sum256(message)
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], response[7:]); err != nil {
		t.Fatalf("party signature does not verify: %v", err)
	}
}

func TestTradeSignatureBindsNativeContextTrailer(t *testing.T) {
	service := testTradeValidationService(t)
	body, party := testTradeRequest(2)
	headerStart := bytes.IndexByte(body, 0) + 1
	binary.BigEndian.PutUint16(body[headerStart:], service.version)
	// Non-palindromic context catches endian mistakes and signing only party.
	copy(body[headerStart+2:headerStart+6], []byte{0x12, 0x34, 0, 1})
	response := performTradeRequest(service.serveValidate, http.MethodPost, body)
	if response.Code != 200 || response.Body.Len() != 263 {
		t.Fatalf("unexpected response: %d/%d", response.Code, response.Body.Len())
	}
	signature := response.Body.Bytes()[7:]
	message := append(append([]byte(nil), party...), 0x12, 0x34, 0, 1)
	digest := sha256.Sum256(message)
	if err := rsa.VerifyPKCS1v15(&service.key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("native message signature: %v", err)
	}
	for _, wrong := range [][]byte{party, append(append([]byte(nil), party...), 0x34, 0x12, 0, 1), append(append([]byte(nil), party...), 0x12, 0x34, 0, 2)} {
		digest := sha256.Sum256(wrong)
		if rsa.VerifyPKCS1v15(&service.key.PublicKey, crypto.SHA256, digest[:], signature) == nil {
			t.Fatal("signature accepted a different message or context")
		}
	}
}

func TestTradeKeyDownloadDoesNotUpgradeOtherClients(t *testing.T) {
	service := testTradeValidationService(t)
	performTradeRequest(service.servePublicKey, http.MethodPost, []byte("client\x00"))
	body, _ := testTradeRequest(1)
	response := performTradeRequest(service.serveValidate, http.MethodPost, body)
	if !bytes.Equal(response.Body.Bytes(), []byte{2}) {
		t.Fatal("stale version accepted after another client downloaded key")
	}
	if response := performTradeRequest(service.servePublicKey, http.MethodGet, nil); response.Code != 405 {
		t.Fatal("public_key must use POST")
	}
	other, err := newTradeValidationService(service.key)
	if err != nil || other.version != service.version {
		t.Fatal("key version changed across service restart")
	}
}

func TestTradeValidationRejectsMalformedRequests(t *testing.T) {
	service := testTradeValidationService(t)
	valid, _ := testTradeRequest(1)
	tests := [][]byte{
		nil,
		[]byte("missing-nul"),
		append([]byte(nil), valid[:len(valid)-1]...),
		append(append([]byte(nil), valid...), 0),
	}
	zero, _ := testTradeRequest(0)
	tests = append(tests, zero)
	tooMany, _ := testTradeRequest(tradeMaxPartySize + 1)
	tests = append(tests, tooMany)
	for index, body := range tests {
		response := performTradeRequest(service.serveValidate, http.MethodPost, body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("case %d status = %d, want 400", index, response.Code)
		}
	}
}

func TestTradeValidationKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trade-validation.pem")
	first, err := loadOrCreateTradeValidationKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateTradeValidationKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.N.Cmp(second.N) != 0 || first.D.Cmp(second.D) != 0 {
		t.Fatal("reloaded key differs from generated key")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(encoded)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("unexpected key PEM block: %#v", block)
	}
}

// Optional public, synthetic fixture consumed by scripts/verify-violet-trade-native.py.
// No private key or real game data is exported.
func TestTradeNativeVerifierFixture(t *testing.T) {
	path := os.Getenv("VIOLET_TRADE_NATIVE_FIXTURE")
	if path == "" {
		t.Skip("native verifier fixture export not requested")
	}
	service := testTradeValidationService(t)
	fixtures := make([]map[string]interface{}, 0, 3)
	for _, count := range []uint16{1, 2, 6} {
		body, party := testTradeRequest(count)
		h := bytes.IndexByte(body, 0) + 1
		binary.BigEndian.PutUint16(body[h:], service.version)
		binary.BigEndian.PutUint16(body[h+2:], 0x1234)
		response := performTradeRequest(service.serveValidate, http.MethodPost, body)
		if response.Code != 200 || response.Body.Len() != 263 {
			t.Fatal("could not produce signature fixture")
		}
		legacyHash := sha256.Sum256(party)
		legacy, err := rsa.SignPKCS1v15(rand.Reader, service.key, crypto.SHA256, legacyHash[:])
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&service.key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, map[string]interface{}{
			"party": hex.EncodeToString(party), "context": 0x1234,
			"public_key_der":   hex.EncodeToString(der),
			"signature":        hex.EncodeToString(response.Body.Bytes()[7:]),
			"legacy_signature": hex.EncodeToString(legacy),
		})
	}
	encoded, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTradeTLSResponsesCompleteAndClose(t *testing.T) {
	service := testTradeValidationService(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/validate", service.serveValidate)
	mux.HandleFunc("/v1/public_key", service.servePublicKey)
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	client := server.Client()
	body, _ := testTradeRequest(2)
	for _, path := range []string{"/v1/validate", "/v1/public_key", "/v1/validate"} {
		response, err := client.Post(server.URL+path, "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || !response.Close || response.ContentLength != int64(len(data)) {
			t.Fatalf("incomplete/non-closing response: path=%s status=%d close=%v length=%d/%d err=%v", path, response.StatusCode, response.Close, response.ContentLength, len(data), readErr)
		}
		if path == "/v1/public_key" {
			if len(data) < 3 {
				t.Fatal("short public key")
			}
			binary.BigEndian.PutUint16(body[bytes.IndexByte(body, 0)+1:], binary.BigEndian.Uint16(data[:2]))
		} else if len(data) != 1 && len(data) != 263 {
			t.Fatalf("unexpected validation response length %d", len(data))
		}
	}
}
