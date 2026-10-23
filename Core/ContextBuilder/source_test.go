package contextbuilder_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type resolverFunc func(context.Context, contextbuilder.ContextSource) (contextbuilder.ResolvedSource, error)

func (resolve resolverFunc) Resolve(ctx context.Context, source contextbuilder.ContextSource) (contextbuilder.ResolvedSource, error) {
	return resolve(ctx, source)
}

func TestBuildSourceResolvesSourceNeutralRecords(t *testing.T) {
	source := contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "run-one"}
	resolver := resolverFunc(func(_ context.Context, requested contextbuilder.ContextSource) (contextbuilder.ResolvedSource, error) {
		if requested != source {
			t.Fatalf("requested source = %+v", requested)
		}
		return contextbuilder.ResolvedSource{Source: source, Records: []contextbuilder.ContextRecord{
			{ID: "old", Source: source, Sequence: 0, Role: contextbuilder.RoleUser, Content: "old"},
			{ID: "new", Source: source, Sequence: 1, Role: contextbuilder.RoleAssistant, Content: "new"},
		}}, nil
	})
	builder, err := contextbuilder.NewWithResolver(resolver, contentLengthEstimator{}, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: source, TokenBudget: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "" || len(result.Messages) != 1 || result.Messages[0].Content != "new" {
		t.Fatalf("result = %+v", result)
	}
	if *result.Metadata.WindowStartSequence != 1 || *result.Metadata.WindowEndSequence != 1 || result.Metadata.OmittedMessages[0].ID != "old" {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

func TestBuildCompatibilityUsesInteractionSessionSource(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "old"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "new"},
	)
	legacy, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 3})
	if err != nil {
		t.Fatal(err)
	}
	generalized, err := fx.builder.BuildSource(context.Background(), contextbuilder.SourceRequest{
		Source:      contextbuilder.InteractionSessionSource(created.ID),
		TokenBudget: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, generalized) {
		t.Fatalf("legacy result = %+v, generalized result = %+v", legacy, generalized)
	}
}

func TestBuildSourceRejectsInvalidMissingAndUnsupportedSources(t *testing.T) {
	fx := newFixture(t)
	tests := []struct {
		name   string
		source contextbuilder.ContextSource
		want   error
	}{
		{name: "invalid kind", source: contextbuilder.ContextSource{Kind: "unknown", ID: "value"}, want: contextbuilder.ErrInvalidSource},
		{name: "empty ID", source: contextbuilder.ContextSource{Kind: contextbuilder.SourceInteractionSession}, want: contextbuilder.ErrInvalidSource},
		{name: "missing session", source: contextbuilder.InteractionSessionSource(session.ID("missing")), want: contextbuilder.ErrSourceNotFound},
		{name: "unsupported cognition run", source: contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "run"}, want: contextbuilder.ErrSourceUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fx.builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: test.source, TokenBudget: 10})
			if !errors.Is(err, test.want) {
				t.Fatalf("BuildSource error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuildMissingSessionPreservesCompatibilityError(t *testing.T) {
	fx := newFixture(t)
	_, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: "missing", TokenBudget: 10})
	if !errors.Is(err, contextbuilder.ErrSourceNotFound) || !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Build error = %v", err)
	}
}

func TestBuildSourcePropagatesAuthorizationFailure(t *testing.T) {
	source := contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "private"}
	builder, err := contextbuilder.NewWithResolver(resolverFunc(func(context.Context, contextbuilder.ContextSource) (contextbuilder.ResolvedSource, error) {
		return contextbuilder.ResolvedSource{}, contextbuilder.ErrSourceUnauthorized
	}), contentLengthEstimator{}, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: source, TokenBudget: 10})
	if !errors.Is(err, contextbuilder.ErrSourceUnauthorized) {
		t.Fatalf("BuildSource error = %v", err)
	}
}

func TestBuildSourceRejectsCrossSourceResolverOutput(t *testing.T) {
	source := contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "requested"}
	foreign := contextbuilder.ContextSource{Kind: contextbuilder.SourceCognitionRun, ID: "foreign"}
	tests := []struct {
		name     string
		resolved contextbuilder.ResolvedSource
	}{
		{name: "resolved identity", resolved: contextbuilder.ResolvedSource{Source: foreign}},
		{name: "record identity", resolved: contextbuilder.ResolvedSource{Source: source, Records: []contextbuilder.ContextRecord{
			{ID: "foreign-record", Source: foreign, Sequence: 0, Role: contextbuilder.RoleUser, Content: "secret"},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder, err := contextbuilder.NewWithResolver(resolverFunc(func(context.Context, contextbuilder.ContextSource) (contextbuilder.ResolvedSource, error) {
				return test.resolved, nil
			}), contentLengthEstimator{}, contextbuilder.SlidingWindowPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = builder.BuildSource(context.Background(), contextbuilder.SourceRequest{Source: source, TokenBudget: 10})
			if !errors.Is(err, contextbuilder.ErrSourceIsolation) {
				t.Fatalf("BuildSource error = %v", err)
			}
		})
	}
}
