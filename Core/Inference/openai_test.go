package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeChatHandler returns a canned chat completion response.
func fakeChatHandler(t *testing.T, statusCode int, body any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if body != nil {
			enc := json.NewEncoder(w)
			if err := enc.Encode(body); err != nil {
				t.Fatalf("encode response: %v", err)
			}
		}
	}
}

func TestOpenAIProvider_Success(t *testing.T) {
	respBody := chatCompletionResponse{
		Choices: []choice{
			{
				FinishReason: "length",
				Index:        0,
				Message:      message{Role: "assistant", Content: "Once upon a time."},
			},
		},
		Usage: usage{CompletionTokens: 4, PromptTokens: 5, TotalTokens: 9},
	}
	srv := httptest.NewServer(fakeChatHandler(t, http.StatusOK, respBody))
	defer srv.Close()

	provider := NewOpenAIProvider(
		WithBaseURL(srv.URL),
		WithMaxTokens(50),
	)
	result, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{
			{Role: "user", Content: "Tell a story"},
		},
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if result.Content != "Once upon a time." {
		t.Errorf("expected 'Once upon a time.', got %q", result.Content)
	}
	if result.ProviderID != OpenAIProviderID {
		t.Errorf("expected provider %s, got %s", OpenAIProviderID, result.ProviderID)
	}
	if result.TokensUsed != 4 {
		t.Errorf("expected 4 tokens used, got %d", result.TokensUsed)
	}
}

func TestOpenAIProvider_EmptyChoices(t *testing.T) {
	respBody := chatCompletionResponse{
		Choices: []choice{},
		Usage:   usage{},
	}
	srv := httptest.NewServer(fakeChatHandler(t, http.StatusOK, respBody))
	defer srv.Close()

	provider := NewOpenAIProvider(WithBaseURL(srv.URL))
	_, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "empty choices") {
		t.Errorf("expected 'empty choices' error, got: %v", err)
	}
}

func TestOpenAIProvider_EmptyContent(t *testing.T) {
	respBody := chatCompletionResponse{
		Choices: []choice{
			{FinishReason: "stop", Index: 0, Message: message{Role: "assistant", Content: ""}},
		},
		Usage: usage{},
	}
	srv := httptest.NewServer(fakeChatHandler(t, http.StatusOK, respBody))
	defer srv.Close()

	provider := NewOpenAIProvider(WithBaseURL(srv.URL))
	_, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if !strings.Contains(err.Error(), "empty content") {
		t.Errorf("expected 'empty content' error, got: %v", err)
	}
}

func TestOpenAIProvider_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(fakeChatHandler(t, http.StatusInternalServerError, map[string]string{
		"error": "internal error",
	}))
	defer srv.Close()

	provider := NewOpenAIProvider(WithBaseURL(srv.URL))
	_, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected '500' in error, got: %v", err)
	}
}

func TestOpenAIProvider_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{this is not json`))
	}))
	defer srv.Close()

	provider := NewOpenAIProvider(WithBaseURL(srv.URL))
	_, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
}

func TestOpenAIProvider_ContextCancellation(t *testing.T) {
	respBody := chatCompletionResponse{
		Choices: []choice{
			{FinishReason: "length", Index: 0, Message: message{Role: "assistant", Content: "hello"}},
		},
	}
	srv := httptest.NewServer(fakeChatHandler(t, http.StatusOK, respBody))
	defer srv.Close()

	provider := NewOpenAIProvider(WithBaseURL(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := provider.Infer(ctx, Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestOpenAIProvider_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the context timeout
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	provider := NewOpenAIProvider(
		WithBaseURL(srv.URL),
		WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := provider.Infer(ctx, Request{
		Model: "default",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for timeout, got nil")
	}
}

func TestOpenAIProvider_WithOptions(t *testing.T) {
	provider := NewOpenAIProvider(
		WithBaseURL("http://example.com:4321"),
		WithMaxTokens(100),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
	)
	if provider.baseURL != "http://example.com:4321" {
		t.Errorf("expected base URL http://example.com:4321, got %s", provider.baseURL)
	}
	if provider.maxTokens != 100 {
		t.Errorf("expected max tokens 100, got %d", provider.maxTokens)
	}
	if provider.client.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", provider.client.Timeout)
	}
	if provider.ID() != OpenAIProviderID {
		t.Errorf("expected provider ID %s, got %s", OpenAIProviderID, provider.ID())
	}
}