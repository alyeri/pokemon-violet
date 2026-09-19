package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in because it requires the local emulator source and .NET SDK.
// The C# harness links SslManagedSocketConnection verbatim and talks to these
// real handlers over TLS with deliberately fragmented guest reads.
func TestTradeEmulatorSSLInterop(t *testing.T) {
	project := os.Getenv("VIOLET_TRADE_TLS_PROJECT")
	if project == "" {
		t.Skip("emulator SSL interoperability harness not requested")
	}
	service := testTradeValidationService(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/validate", service.serveValidate)
	mux.HandleFunc("/v1/public_key", service.servePublicKey)
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "dotnet", "run", "--project", project, "-c", "Release", "--", "--server", server.URL, "--nonblocking", "--native-upload").CombinedOutput()
	if err != nil {
		t.Fatalf("emulator SSL interoperability: %v\n%s", err, output)
	}
	t.Log(string(output))
}
