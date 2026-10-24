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
	ErrUnknownInferenceTask      = errors.New("config: unknown inference task")
)

// InferenceTask identifies a Core-owned reason for invoking inference.
type InferenceTask string

const (
	InferenceTaskInteraction     InferenceTask = "interaction"
	InferenceTaskOrient          InferenceTask = "orient"
	InferenceTaskPlan            InferenceTask = "plan"
	InferenceTaskDirectCognition InferenceTask = "direct_cognition"
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
	tasks        map[InferenceTask]string
}

// ValidateModelConfiguration verifies that a resolved model is complete and
// usable at a runtime inference boundary.
func ValidateModelConfiguration(model ModelConfiguration) error {
	if strings.TrimSpace(model.Name) == "" || strings.TrimSpace(model.Provider) == "" ||
		strings.TrimSpace(model.Model) == "" || strings.TrimSpace(model.Endpoint) == "" {
		return fmt.Errorf("%w: resolved model requires name, provider, model, and endpoint", ErrInvalidModelConfiguration)
	}
	if _, err := model.Budget.InputTokens(); err != nil {
		return fmt.Errorf("%w: model %q budget: %w", ErrInvalidModelConfiguration, model.Name, err)
	}
	return nil
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
		if model.Budget.ContextTokens == 0 {
			model.Budget.ContextTokens = inference.DefaultContextTokens
		}
		if err := ValidateModelConfiguration(model); err != nil {
			return nil, err
		}
		models[name] = model
	}
	if _, ok := models[defaultModel]; !ok {
		return nil, fmt.Errorf("%w: default model %q", ErrUnknownModelReference, defaultModel)
	}
	tasks := make(map[InferenceTask]string, len(config.Tasks))
	for task, modelName := range config.Tasks {
		if !validInferenceTask(task) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownInferenceTask, task)
		}
		modelName = strings.TrimSpace(modelName)
		if _, ok := models[modelName]; !ok {
			return nil, fmt.Errorf("%w: task %q references %q", ErrUnknownModelReference, task, modelName)
		}
		tasks[task] = modelName
	}
	return &ModelResolver{defaultModel: defaultModel, models: models, tasks: tasks}, nil
}

func validInferenceTask(task InferenceTask) bool {
	switch task {
	case InferenceTaskInteraction, InferenceTaskOrient, InferenceTaskPlan, InferenceTaskDirectCognition:
		return true
	default:
		return false
	}
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

// ResolveTask returns a task's explicitly assigned model, or the configured
// default when the known task has no assignment.
func (resolver *ModelResolver) ResolveTask(task InferenceTask) (ModelConfiguration, error) {
	if !validInferenceTask(task) {
		return ModelConfiguration{}, fmt.Errorf("%w: %q", ErrUnknownInferenceTask, task)
	}
	name, ok := resolver.tasks[task]
	if !ok {
		name = resolver.defaultModel
	}
	return resolver.Resolve(name)
}
