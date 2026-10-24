package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

var (
	ErrInvalidModelConfiguration = errors.New("config: invalid model configuration")
	ErrUnknownModelReference     = errors.New("config: unknown model reference")
)

// ModelConfiguration is one named, static inference target. Budget controls
// Core consumption and is never expanded from provider-advertised capacity.
type ModelConfiguration struct {
	Name     string                       `json:"-"`
	Provider string                       `json:"provider"`
	Model    string                       `json:"model"`
	Endpoint string                       `json:"endpoint"`
	Budget   inference.ModelContextBudget `json:"budget"`
}

// ModelResolver validates and resolves an immutable snapshot of named models.
type ModelResolver struct {
	defaultModel string
	models       map[string]ModelConfiguration
}

// UnmarshalJSON keeps inference configuration strict without changing the
// historically permissive decoding of unrelated Core configuration sections.
func (config *InferenceConfig) UnmarshalJSON(data []byte) error {
	type inferenceConfig InferenceConfig
	decoded := inferenceConfig(*config)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*config = InferenceConfig(decoded)
	return nil
}

func NewModelResolver(config InferenceConfig) (*ModelResolver, error) {
	defaultModel := strings.TrimSpace(config.DefaultModel)
	if defaultModel == "" {
		return nil, fmt.Errorf("%w: default_model is required", ErrInvalidModelConfiguration)
	}
	models := make(map[string]ModelConfiguration, len(config.Models))
	for name, model := range config.Models {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("%w: model name must be non-empty without surrounding whitespace", ErrInvalidModelConfiguration)
		}
		model.Name = name
		model.Provider = strings.TrimSpace(model.Provider)
		model.Model = strings.TrimSpace(model.Model)
		model.Endpoint = strings.TrimSpace(model.Endpoint)
		if model.Provider == "" || model.Model == "" || model.Endpoint == "" {
			return nil, fmt.Errorf("%w: model %q requires provider, model, and endpoint", ErrInvalidModelConfiguration, name)
		}
		if model.Budget.ContextTokens == 0 {
			model.Budget.ContextTokens = inference.DefaultContextTokens
		}
		if _, err := model.Budget.InputTokens(); err != nil {
			return nil, fmt.Errorf("%w: model %q budget: %w", ErrInvalidModelConfiguration, name, err)
		}
		models[name] = model
	}
	if _, ok := models[defaultModel]; !ok {
		return nil, fmt.Errorf("%w: default model %q", ErrUnknownModelReference, defaultModel)
	}
	return &ModelResolver{defaultModel: defaultModel, models: models}, nil
}

func (resolver *ModelResolver) Resolve(name string) (ModelConfiguration, error) {
	model, ok := resolver.models[name]
	if !ok {
		return ModelConfiguration{}, fmt.Errorf("%w: %q", ErrUnknownModelReference, name)
	}
	return model, nil
}

func (resolver *ModelResolver) Default() ModelConfiguration {
	return resolver.models[resolver.defaultModel]
}
