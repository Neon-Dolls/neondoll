package inference

import (
	"context"
	"testing"
)

func TestMockProvider(t *testing.T) {
	provider := NewMockProvider("test-provider", "Hello, world!")
	resp, err := provider.Infer(context.Background(), Request{
		Model: "default",
		Messages: []Message{
			{Role: "user", Content: "Hi"},
		},
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if resp.Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", resp.Content)
	}
	if resp.ProviderID != "test-provider" {
		t.Errorf("expected test-provider, got %s", resp.ProviderID)
	}
	if resp.TokensUsed != 1 {
		t.Errorf("expected 1 token, got %d", resp.TokensUsed)
	}
}

func TestProviderID(t *testing.T) {
	provider := NewMockProvider("my-provider", "ok")
	if provider.ID() != "my-provider" {
		t.Errorf("expected my-provider, got %s", provider.ID())
	}
}

func TestMockProviderMultipleMessages(t *testing.T) {
	provider := NewMockProvider("multi", "response")
	resp, err := provider.Infer(context.Background(), Request{
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if resp.TokensUsed != 2 {
		t.Errorf("expected 2 tokens (message count), got %d", resp.TokensUsed)
	}
}