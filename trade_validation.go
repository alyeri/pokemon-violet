package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	tradePartyEntrySize = 0x148
	tradeSignatureSize  = 0x100
	tradeMaxPartySize   = 6
	tradeMaxRequestSize = 8 << 10
)

type tradeValidationRequest struct {
	prefix  string
	header  [6]byte
	party   []byte
	entries uint16
}

type tradeValidationService struct {
	key     *rsa.PrivateKey
	version uint16
}

func newTradeValidationService(key *rsa.PrivateKey) (*tradeValidationService, error) {
	if key == nil || key.N.BitLen() != tradeSignatureSize*8 {
		return nil, fmt.Errorf("trade validation requires a %d-bit RSA key", tradeSignatureSize*8)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(der)
	version := binary.BigEndian.Uint16(digest[:2])
	if version == 0 {
		version = 1
	}
	return &tradeValidationService{key: key, version: version}, nil
}

func loadOrCreateTradeValidationKey(path string) (*rsa.PrivateKey, error) {
	if encoded, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(encoded)
		if block == nil {
			return nil, errors.New("trade validation key is not PEM")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse trade validation key: %w", err)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok || key.N.BitLen() != tradeSignatureSize*8 {
			return nil, errors.New("trade validation key is not RSA-2048")
		}
		if err := key.Validate(); err != nil {
			return nil, fmt.Errorf("validate trade validation key: %w", err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read trade validation key: %w", err)
	}

	key, err := rsa.GenerateKey(rand.Reader, tradeSignatureSize*8)
	if err != nil {
		return nil, fmt.Errorf("generate trade validation key: %w", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal trade validation key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create trade validation key directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		return nil, fmt.Errorf("write trade validation key: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return nil, fmt.Errorf("install trade validation key: %w", err)
	}
	return key, nil
}

func parseTradeValidationRequest(body []byte) (tradeValidationRequest, error) {
	var request tradeValidationRequest
	nul := -1
	for index, value := range body {
		if value == 0 {
			nul = index
			break
		}
	}
	if nul <= 0 || nul+9 > len(body) {
		return request, errors.New("missing prefix or fixed header")
	}
	request.prefix = string(body[:nul])
	copy(request.header[:], body[nul+1:nul+7])
	request.entries = binary.BigEndian.Uint16(body[nul+7 : nul+9])
	if request.entries == 0 || request.entries > tradeMaxPartySize {
		return request, fmt.Errorf("party count %d is outside 1..%d", request.entries, tradeMaxPartySize)
	}
	expected := nul + 9 + int(request.entries)*tradePartyEntrySize
	if len(body) != expected {
		return request, fmt.Errorf("body has %d bytes, expected %d", len(body), expected)
	}
	request.party = append([]byte(nil), body[nul+9:]...)
	return request, nil
}

func (s *tradeValidationService) publicKeyResponse() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(der)
	if len(encoded) > int(^uint16(0)) {
		return nil, errors.New("encoded public key is too large")
	}
	// Native 0x16d0b00 stores the first uint16 as key version and uses strlen
	// on the following Base64. Include its terminating NUL in the HTTP body.
	response := make([]byte, 3+len(encoded))
	binary.BigEndian.PutUint16(response[:2], s.version)
	copy(response[2:], encoded)
	return response, nil
}

func (s *tradeValidationService) servePublicKey(w http.ResponseWriter, r *http.Request) {
	// 0x1d5f5a8 uses the same POST builder as validate, with a NUL string body.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	response, err := s.publicKeyResponse()
	if err != nil {
		http.Error(w, "public key unavailable", http.StatusInternalServerError)
		return
	}
	log.Printf("[VIOLET VALIDATE] public_key version=%d response_bytes=%d", s.version, len(response))
	writeTradeBinary(w, r, response)
}

func (s *tradeValidationService) serveValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tradeMaxRequestSize))
	if err != nil {
		http.Error(w, "invalid validation request", http.StatusBadRequest)
		return
	}
	request, err := parseTradeValidationRequest(body)
	if err != nil {
		http.Error(w, "invalid validation request", http.StatusBadRequest)
		return
	}

	clientVersion := binary.BigEndian.Uint16(request.header[:2])
	if clientVersion != s.version {
		// Discriminant 2 instructs Violet to download /v1/public_key and retry.
		log.Printf("[VIOLET VALIDATE] validate client_version=%d server_version=%d entries=%d action=refresh_key", clientVersion, s.version, request.entries)
		writeTradeBinary(w, r, []byte{2})
		return
	}

	// Native verifier 0x1d9e544 serializes the same 0x148-byte records, then
	// appends the uint16 context in network order and 00 01 (0x1d9e61c..634).
	// These are request.header[2:6]; the key version and count are not hashed.
	// 0x1d9e78c..8ec checks PKCS#1 v1.5 padding and the SHA-256 digest.
	hash := sha256.New()
	_, _ = hash.Write(request.party)
	_, _ = hash.Write(request.header[2:6])
	digest := hash.Sum(nil)
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest)
	if err != nil {
		http.Error(w, "validation signing failed", http.StatusInternalServerError)
		return
	}
	response := make([]byte, 7+tradeSignatureSize)
	// byte 0 == accepted, bytes 1..4 are reserved, bytes 5..6 contain the
	// big-endian rejection-detail count (zero on success).
	copy(response[7:], signature)
	log.Printf("[VIOLET VALIDATE] validate version=%d entries=%d signed_bytes=%d response_bytes=%d action=accepted", clientVersion, request.entries, len(request.party)+4, len(response))
	writeTradeBinary(w, r, response)
}

func writeTradeBinary(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	// The current emulator checks encrypted socket readability before reading
	// SslStream and reports Pending() == 0. A split guest read can strand the
	// rest of this response in SslStream while a keep-alive socket is idle.
	// Closing these finite REST exchanges makes EOF readable and allows the
	// guest to drain the response. No gRPC/Gamesync connection is affected.
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	written, err := w.Write(body)
	if err == nil {
		err = http.NewResponseController(w).Flush()
	}
	log.Printf("[VIOLET VALIDATE] response path=%s remote=%s written_bytes=%d expected_bytes=%d flush_error=%v connection=close", r.URL.Path, r.RemoteAddr, written, len(body), err)
	if err == nil {
		drainTradeUploadBeforeClose(w, r)
	}
}

// Violet's native upload callback (main 0x2adda90) copies min(capacity,
// remaining) but returns capacity. Consequently it sends a 64 KiB buffer even
// when Content-Length is much smaller. Closing with unread TLS records resets
// the TCP connection on Windows and loses the response. Consume surplus only
// after this finite response, never as another request or validation payload.
// Both bytes and time are bounded; non-hijackable test/HTTP2 writers are untouched.
func drainTradeUploadBeforeClose(w http.ResponseWriter, r *http.Request) {
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		return
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(buffered.Reader, 64<<10))
	log.Printf("[VIOLET VALIDATE] upload_drain path=%s surplus_bytes=%d limit_bytes=65536", r.URL.Path, n)
}
