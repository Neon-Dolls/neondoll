package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

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
	for _, item := range response.Results {
		record := recallRecord(item)
		if !kindAllowed(req.Kinds, record.Metadata.Kind) || !sourceAllowed(req.SourceTypes, record.Source.Type) {
			continue
		}
		result.Matches = append(result.Matches, memory.RecallMatch{Reference: record.Reference(), Record: &record})
		if len(result.Matches) == int(req.Limit) {
			break
		}
	}
	return result, result.Validate(req)
}

func recallRecord(item recallItem) memory.Record {
	created := parseTime(item.MentionedAt, item.OccurredStart, item.OccurredEnd)
	sourceType := "hindsight"
	if value := item.Metadata["neondoll_source_type"]; value != "" {
		sourceType = value
	}
	kind := item.Type
	if value := item.Metadata["neondoll_kind"]; value != "" {
		kind = value
	}
	if kind == "" {
		kind = "retained-memory"
	}
	data, _ := json.Marshal(item.Text)
	extensions := map[string]json.RawMessage{}
	if item.DocumentID != "" {
		encoded, _ := json.Marshal(item.DocumentID)
		extensions["hindsight.document_id"] = encoded
	}
	sourceRef := item.DocumentID
	if sourceType == "unknown" {
		sourceRef = ""
	}
	return memory.Record{SchemaVersion: memory.SchemaVersion, ID: item.ID, Revision: 1,
		CreatedAt: created, Source: memory.Source{Type: sourceType, Ref: sourceRef},
		Content:  memory.Content{MediaType: "text/plain", Transport: memory.TransportInline, Data: data},
		Metadata: memory.Metadata{Kind: kind, Tags: item.Tags, Extensions: extensions}}
}

func parseTime(values ...string) time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return time.Unix(0, 0).UTC()
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
