// Package self owns the bounded developing-Self contract and revision
// lifecycle. Soul, Memory, authority, and persistence remain separate.
package self

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/DollState"
)

const DefaultMaxTokens = 2000

var (
	ErrInvalidConfiguration = errors.New("self: invalid configuration")
	ErrInvalidRevision      = errors.New("self: invalid revision")
	ErrContentTooLarge      = errors.New("self: content exceeds configured token budget")
	ErrStaleRevision        = errors.New("self: stale revision")
	ErrNotFound             = errors.New("self: Doll not found")
)

// EstimatedTokens uses the same conservative provider-independent estimate as
// Context Builder, without message framing. Validation never truncates.
func EstimatedTokens(content string) int {
	return (utf8.RuneCountInString(content) + 3) / 4
}

func ValidateActive(value dollstate.Self, maxTokens int) error {
	if maxTokens <= 0 {
		return ErrInvalidConfiguration
	}
	if strings.TrimSpace(value.Content) == "" || strings.TrimSpace(value.RevisionID) == "" || value.Revision == 0 ||
		value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return ErrInvalidRevision
	}
	if EstimatedTokens(value.Content) > maxTokens {
		return ErrContentTooLarge
	}
	return nil
}

// ContextText returns the one active portable Self snapshot. It contains the
// revision identity required for an explicit replacement and no history.
func ContextText(value dollstate.Self) string {
	if strings.TrimSpace(value.Content) == "" {
		return ""
	}
	return fmt.Sprintf("Developing Self — current active revision (distinct from given Soul, Memory, and authority):\nrevision_id: %s\nrevision: %d\n\n%s",
		value.RevisionID, value.Revision, value.Content)
}
