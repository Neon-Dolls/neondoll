package packagesource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

type SkillInstaller interface {
	Install(context.Context, skill.ID, string, []byte) error
}

type ExtensionInstaller interface {
	InstallExtension(context.Context, []byte, []byte) error
}

type ExecutionItem struct {
	Package   Reference `json:"package"`
	Installed bool      `json:"installed"`
	Error     string    `json:"error,omitempty"`
}

type ExecutionReport struct {
	PlanID    string          `json:"plan_id"`
	Completed bool            `json:"completed"`
	Partial   bool            `json:"partial"`
	Items     []ExecutionItem `json:"items"`
}

type Progress func(ExecutionReport) error

type Executor struct {
	mu         sync.RWMutex
	registry   *Registry
	skills     SkillInstaller
	extensions ExtensionInstaller
	bounds     ArtifactBounds
}

func (e *Executor) SetExtensionInstaller(installer ExtensionInstaller) {
	e.mu.Lock()
	e.extensions = installer
	e.mu.Unlock()
}

func NewExecutor(registry *Registry, skills SkillInstaller, extensions ExtensionInstaller, bounds ArtifactBounds) (*Executor, error) {
	if registry == nil || bounds.MaxBytes <= 0 || bounds.MaxItems == 0 {
		return nil, ErrInvalid
	}
	return &Executor{registry: registry, skills: skills, extensions: extensions, bounds: bounds}, nil
}

func (e *Executor) Execute(ctx context.Context, plan InstallationPlan) (ExecutionReport, error) {
	return e.ExecuteWithProgress(ctx, plan, nil)
}

func (e *Executor) ExecuteWithProgress(ctx context.Context, plan InstallationPlan, progress Progress) (ExecutionReport, error) {
	report := ExecutionReport{PlanID: plan.ID}
	if err := plan.Validate(); err != nil {
		return report, err
	}
	if plan.Approval.State != ApprovalReady {
		return report, ErrNotReady
	}
	if uint32(len(plan.Items)) > e.bounds.MaxItems {
		return report, ErrBounds
	}
	source, ok := e.registry.Source(plan.SourceID)
	if !ok {
		return report, ErrNotFound
	}
	type acquired struct {
		item PlanItem
		data []byte
	}
	artifacts := make([]acquired, 0, len(plan.Items))
	var total int64
	for _, item := range plan.Items {
		if item.Artifact.Size > e.bounds.MaxBytes-total {
			return report, ErrBounds
		}
		var buffer bytes.Buffer
		limited := &boundedWriter{writer: &buffer, remaining: item.Artifact.Size}
		if err := source.Acquire(ctx, item.Artifact, limited); err != nil {
			return report, err
		}
		if limited.exceeded || int64(buffer.Len()) != item.Artifact.Size {
			return report, ErrBounds
		}
		total += int64(buffer.Len())
		artifacts = append(artifacts, acquired{item: item, data: append([]byte(nil), buffer.Bytes()...)})
	}
	for _, artifact := range artifacts {
		result := ExecutionItem{Package: artifact.item.Package}
		var err error
		switch artifact.item.Kind {
		case KindSkill:
			if e.skills == nil {
				err = ErrInvalid
			} else {
				err = e.skills.Install(ctx, skill.ID(artifact.item.Package.ID), plan.Operation, artifact.data)
			}
		case KindExtension:
			e.mu.RLock()
			installer := e.extensions
			e.mu.RUnlock()
			if installer == nil {
				err = ErrInvalid
			} else {
				err = installer.InstallExtension(ctx, artifact.item.ExtensionManifest, artifact.data)
			}
		default:
			err = ErrInvalid
		}
		if err != nil {
			result.Error = err.Error()
			report.Items = append(report.Items, result)
			report.Partial = installedCount(report.Items) > 0
			installErr := fmt.Errorf("package source: install %s: %w", artifact.item.Package.ID, err)
			if progress != nil {
				if progressErr := progress(report); progressErr != nil {
					return report, errors.Join(installErr, progressErr)
				}
			}
			return report, installErr
		}
		result.Installed = true
		report.Items = append(report.Items, result)
		report.Partial = len(report.Items) < len(artifacts)
		if progress != nil {
			if err := progress(report); err != nil {
				return report, err
			}
		}
	}
	report.Completed = true
	return report, nil
}

func installedCount(items []ExecutionItem) int {
	count := 0
	for _, item := range items {
		if item.Installed {
			count++
		}
	}
	return count
}

type boundedWriter struct {
	writer    *bytes.Buffer
	remaining int64
	exceeded  bool
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		w.exceeded = true
		return 0, ErrBounds
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
