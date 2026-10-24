package inference

import "fmt"

// NewProvider constructs one of Core's statically supported providers for a
// resolved model endpoint. It performs no discovery or model selection.
func NewProvider(provider, endpoint string) (Provider, error) {
	switch provider {
	case string(OpenAIProviderID):
		return NewOpenAIProvider(WithBaseURL(endpoint)), nil
	default:
		return nil, fmt.Errorf("inference: unsupported provider %q", provider)
	}
}
