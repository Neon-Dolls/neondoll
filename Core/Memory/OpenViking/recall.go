package openviking

import (
	"context"
	"net/http"
	"strings"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type searchResult struct {
	Memories  []searchHit `json:"memories"`
	Resources []searchHit `json:"resources"`
}
type searchHit struct {
	URI string `json:"uri"`
}

func (p *Provider) Recall(ctx context.Context, req memory.RecallRequest) (memory.RecallResult, error) {
	if err := req.Validate(); err != nil {
		return memory.RecallResult{}, err
	}
	candidateLimit := req.Limit
	if candidateLimit > p.maxRecallCandidates {
		candidateLimit = p.maxRecallCandidates
	}
	if len(req.Kinds) > 0 || len(req.SourceTypes) > 0 {
		candidateLimit = p.maxRecallCandidates
	}
	query := req.Query
	if strings.TrimSpace(query) == "" {
		query = "memory"
	}
	var result searchResult
	body := map[string]any{
		"query": query, "target_uri": p.root, "limit": int(candidateLimit),
		"context_type": []string{"memory"},
	}
	if err := p.call(ctx, http.MethodPost, "/api/v1/search/find", nil, body, &result, nil); err != nil {
		return memory.RecallResult{}, err
	}
	uris := make([]string, 0, len(result.Memories)+len(result.Resources))
	for _, hit := range result.Memories {
		uris = append(uris, hit.URI)
	}
	for _, hit := range result.Resources {
		uris = append(uris, hit.URI)
	}
	out, seen := memory.RecallResult{}, map[string]bool{}
	for _, uri := range uris {
		if err := ctx.Err(); err != nil {
			return memory.RecallResult{}, err
		}
		if len(out.Matches) == int(req.Limit) {
			break
		}
		if seen[uri] || !p.isRecordURI(uri) {
			continue
		}
		seen[uri] = true
		doc, ok, err := p.readDocument(ctx, uri)
		if err != nil {
			return memory.RecallResult{}, err
		}
		if !ok || !matches(doc.Record, req) {
			continue
		}
		record := doc.Record
		out.Matches = append(out.Matches, memory.RecallMatch{Reference: record.Reference(), Record: &record})
	}
	return out, out.Validate(req)
}

func matches(record memory.Record, req memory.RecallRequest) bool {
	if len(req.Kinds) > 0 {
		matched := false
		for _, kind := range req.Kinds {
			if record.Metadata.Kind == kind {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(req.SourceTypes) > 0 {
		matched := false
		for _, source := range req.SourceTypes {
			if record.Source.Type == source {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
