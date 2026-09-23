package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultOpenAITimeout is the default HTTP client timeout.
	DefaultOpenAITimeout = 60 * time.Second

	// DefaultMaxTokens is the default maximum generated tokens.
	DefaultMaxTokens = 256

	// DefaultBaseURL is the default llama.cpp server URL.
	DefaultBaseURL = "http://localhost:8080"
)

// OpenAIProviderID is the provider identifier for the OpenAI-compatible provider.
const OpenAIProviderID ProviderID = "openai"

// chatCompletionRequest mirrors the OpenAI chat completions request body.
type chatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionResponse mirrors the OpenAI chat completions response body.
type chatCompletionResponse struct {
	Choices []choice `json:"choices"`
	Usage   usage    `json:"usage,omitempty"`
}

type choice struct {
	FinishReason string  `json:"finish_reason"`
	Index        int     `json:"index"`
	Message      message `json:"message"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIProvider sends inference requests to any OpenAI-compatible HTTP API
// (such as llama.cpp server).
type OpenAIProvider struct {
	id        ProviderID
	baseURL   string
	client    *http.Client
	maxTokens int
}

// OpenAIProviderOption configures an OpenAIProvider.
type OpenAIProviderOption func(*OpenAIProvider)

// WithBaseURL sets the base URL for the provider.
func WithBaseURL(url string) OpenAIProviderOption {
	return func(p *OpenAIProvider) {
		p.baseURL = url
	}
}

// WithHTTPClient sets the HTTP client for the provider.
func WithHTTPClient(client *http.Client) OpenAIProviderOption {
	return func(p *OpenAIProvider) {
		p.client = client
	}
}

// WithMaxTokens sets the default max tokens for generation.
func WithMaxTokens(n int) OpenAIProviderOption {
	return func(p *OpenAIProvider) {
		p.maxTokens = n
	}
}

// NewOpenAIProvider creates a new OpenAI-compatible HTTP provider.
func NewOpenAIProvider(opts ...OpenAIProviderOption) *OpenAIProvider {
	p := &OpenAIProvider{
		id:        OpenAIProviderID,
		baseURL:   DefaultBaseURL,
		maxTokens: DefaultMaxTokens,
		client: &http.Client{
			Timeout: DefaultOpenAITimeout,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ID returns the provider identifier.
func (p *OpenAIProvider) ID() ProviderID {
	return p.id
}

// Infer sends a chat completion request and returns the generated response.
func (p *OpenAIProvider) Infer(ctx context.Context, req Request) (*Response, error) {
	maxTokens := p.maxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	chatReq := chatCompletionRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}
	if chatReq.MaxTokens <= 0 {
		chatReq.MaxTokens = DefaultMaxTokens
	}
	if chatReq.Temperature == 0 {
		chatReq.Temperature = 0.7
	}

	for _, m := range req.Messages {
		chatReq.Messages = append(chatReq.Messages, message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("inference openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("inference openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("inference openai: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("inference openai: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inference openai: %s (status %d)",
			truncateString(string(raw), 200), resp.StatusCode)
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(raw, &chatResp); err != nil {
		return nil, fmt.Errorf("inference openai: decode response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("inference openai: empty choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	if content == "" {
		return nil, fmt.Errorf("inference openai: empty content in response")
	}

	result := &Response{
		Content:    content,
		ProviderID: p.id,
		TokensUsed: chatResp.Usage.CompletionTokens,
	}

	return result, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
