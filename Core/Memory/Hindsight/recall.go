package hindsight

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type recallResponse struct {
	Results []recallItem `json:"results"`
}
type recallItem struct {
	ID            string            `json:"id"`
	Text          string            `json:"text"`
	Type          string            `json:"type"`
	Context       string            `json:"context"`
	DocumentID    string            `json:"document_id"`
	MentionedAt   string            `json:"mentioned_at"`
	OccurredStart string            `json:"occurred_start"`
	OccurredEnd   string            `json:"occurred_end"`
	Metadata      map[string]string `json:"metadata"`
	Tags          []string          `json:"tags"`
}

func (p *Provider) Recall(ctx context.Context, req memory.RecallRequest) (memory.RecallResult, error) {
	if err := req.Validate(); err != nil {
		return memory.RecallResult{}, err
	}
	body := map[string]any{"query": req.Query, "max_tokens": p.maxRecallTokens, "budget": "high"}
	if types := hindsightTypes(req.Kinds); len(types) > 0 {
		body["types"] = types
	}
	var response recallResponse
	if err := p.call(ctx, http.MethodPost, p.bankPath()+"/memories/recall", nil, body, &response); err != nil {
		return memory.RecallResult{}, err
	}
	result := memory.RecallResult{}
	seenDocuments := make(map[string]struct{})
	for _, item := range response.Results {
		if item.DocumentID == "" {
			return memory.RecallResult{}, fmt.Errorf("%w: recalled Hindsight unit lacks document identity", memory.ErrInvalidResult)
		}
		if _, duplicate := seenDocuments[item.DocumentID]; duplicate {
			continue
		}
		if len(seenDocuments) == p.maxRecallDocuments {
			return memory.RecallResult{}, fmt.Errorf("%w: recall document lookup limit", memory.ErrTransferLimit)
		}
		seenDocuments[item.DocumentID] = struct{}{}
		doc, found, err := p.getDocument(ctx, item.DocumentID)
		if err != nil {
			return memory.RecallResult{}, err
		}
		if !found {
			return memory.RecallResult{}, fmt.Errorf("%w: recalled Hindsight document is unavailable", memory.ErrInvalidResult)
		}
		state, err := decodeState(doc)
		if err != nil {
			return memory.RecallResult{}, err
		}
		if !kindAllowed(req.Kinds, state.Record.Metadata.Kind) || !sourceAllowed(req.SourceTypes, state.Record.Source.Type) {
			continue
		}
		units, err := p.listDocumentMemories(ctx, item.DocumentID)
		if err != nil {
			return memory.RecallResult{}, err
		}
		record, err := retainedRecord(state.Record, units)
		if err != nil {
			return memory.RecallResult{}, err
		}
		result.Matches = append(result.Matches, memory.RecallMatch{Reference: record.Reference(), Record: &record})
		if len(result.Matches) == int(req.Limit) {
			break
		}
	}
	return result, result.Validate(req)
}
func sourceAllowed(filters []string, value string) bool {
	return len(filters) == 0 || contains(filters, value)
}
func kindAllowed(filters []string, value string) bool {
	return len(filters) == 0 || contains(filters, value)
}
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}
func hindsightTypes(kinds []string) []string {
	var result []string
	for _, kind := range kinds {
		if contains([]string{"world", "experience", "observation"}, kind) {
			result = append(result, strings.ToLower(kind))
		}
	}
	return result
}
