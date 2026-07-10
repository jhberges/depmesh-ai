package api

import (
	"net/http/httptest"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/gate"
)

func TestHealthz(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/healthz")
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, response)
	}
}

func TestUnknownEcosystemIs400(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/v1/vet/cargo/serde")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 400 {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
}
