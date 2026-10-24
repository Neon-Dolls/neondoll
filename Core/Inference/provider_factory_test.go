package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredProviderUsesEndpointAndModelIdentifier(t *testing.T) {
	var received chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := NewProvider("openai", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Infer(context.Background(), Request{
		Model: "configured-model", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 321,
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Model != "configured-model" || received.MaxTokens != 321 {
		t.Fatalf("provider request = %+v", received)
	}
}

func TestConfiguredProviderRejectsUnsupportedProvider(t *testing.T) {
	_, err := NewProvider("unsupported", "http://localhost")
	if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("NewProvider error = %v", err)
	}
}
