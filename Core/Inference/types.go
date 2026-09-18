package inference

import "context"

// ProviderID identifies an inference provider.
type ProviderID string

// Request is a generic inference request.
type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
}

// Message in a conversation.
type Message struct {
	Role    string
	Content string
}

// Response from inference.
type Response struct {
	Content    string
	ProviderID ProviderID
	TokensUsed int
}

// Provider is the interface for inference backends.
type Provider interface {
	Infer(ctx context.Context, req Request) (*Response, error)
	ID() ProviderID
}

// MockProvider for testing.
type MockProvider struct {
	id       ProviderID
	response string
}

func NewMockProvider(id ProviderID, response string) *MockProvider {
	return &MockProvider{id: id, response: response}
}

func (m *MockProvider) Infer(_ context.Context, req Request) (*Response, error) {
	return &Response{
		Content:    m.response,
		ProviderID: m.id,
		TokensUsed: len(req.Messages),
	}, nil
}

func (m *MockProvider) ID() ProviderID { return m.id }