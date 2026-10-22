// Package contextbuilder assembles disposable, bounded model context from a
// persisted Core Interaction Session. It does not own conversation history or
// continuity-bearing Doll state.
package contextbuilder

import (
	"context"
	"errors"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"

	OmittedBudget = "token_budget"
)

var (
	ErrInvalidRequest               = errors.New("context builder: invalid request")
	ErrRequiredContextExceedsBudget = errors.New("context builder: required context exceeds token budget")
)

// Message is provider-neutral, model-ready text context.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// HistoryMessage is one durable conversation record owned by an Interaction
// Session. Sequence, rather than timestamps or row order, defines ordering.
type HistoryMessage struct {
	ID        string     `json:"message_id"`
	SessionID session.ID `json:"session_id"`
	Sequence  int        `json:"sequence"`
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
}

type NewHistoryMessage struct {
	Role    Role
	Content string
}

// HistoryStore is implemented by Core's existing persistence store. Appends
// mutate durable history; Build only uses ListSessionMessages.
type HistoryStore interface {
	AppendSessionMessages(context.Context, session.ID, []NewHistoryMessage) ([]HistoryMessage, error)
	ListSessionMessages(context.Context, session.ID) ([]HistoryMessage, error)
}

type TokenEstimator interface {
	EstimateTokens(Message) int
}

type Builder interface {
	Build(context.Context, Request) (Result, error)
}

type Request struct {
	SessionID          session.ID
	TokenBudget        int
	SystemInstructions []string
}

type OmittedMessage struct {
	ID              string `json:"message_id"`
	Sequence        int    `json:"sequence"`
	EstimatedTokens int    `json:"estimated_tokens"`
	Reason          string `json:"reason"`
}

type Metadata struct {
	TokenBudget             int              `json:"token_budget"`
	EstimatedTokens         int              `json:"estimated_tokens"`
	HistoryMessages         int              `json:"history_messages"`
	IncludedHistoryMessages int              `json:"included_history_messages"`
	OmittedHistoryMessages  int              `json:"omitted_history_messages"`
	IncludedMessageIDs      []string         `json:"included_message_ids,omitempty"`
	OmittedMessages         []OmittedMessage `json:"omitted_messages,omitempty"`
}

type Result struct {
	SessionID session.ID `json:"session_id"`
	Messages  []Message  `json:"messages"`
	Metadata  Metadata   `json:"metadata"`
}
