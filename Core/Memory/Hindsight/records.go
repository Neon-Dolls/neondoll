package hindsight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

const stateKey = "neondoll_memory_state_v1"

type retainedState struct {
	Record  memory.Record     `json:"record"`
	Digest  string            `json:"digest"`
	Replays map[string]string `json:"replays,omitempty"`
}
type document struct {
	ID               string         `json:"id"`
	BankID           string         `json:"bank_id"`
	OriginalText     string         `json:"original_text"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
	MemoryUnitCount  int            `json:"memory_unit_count"`
	DocumentMetadata map[string]any `json:"document_metadata"`
}
type memoryUnit struct {
	ID            string         `json:"id"`
	Text          string         `json:"text"`
	Context       string         `json:"context"`
	FactType      string         `json:"fact_type"`
	DocumentID    string         `json:"document_id"`
	MentionedAt   string         `json:"mentioned_at"`
	OccurredStart string         `json:"occurred_start"`
	OccurredEnd   string         `json:"occurred_end"`
	UpdatedAt     string         `json:"updated_at"`
	Tags          []string       `json:"tags"`
	Metadata      map[string]any `json:"metadata"`
}
type listMemories struct {
	Items  []memoryUnit `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func (p *Provider) Remember(ctx context.Context, req memory.RememberRequest) (memory.RememberResult, error) {
	if err := req.Record.Validate(); err != nil {
		return rejected(err.Error()), nil
	}
	content, payload, err := p.readContent(ctx, req.Record, req.Payload)
	if err != nil {
		if recordError(err) {
			return rejected(err.Error()), nil
		}
		return memory.RememberResult{}, err
	}
	digest, err := recordDigest(req.Record, payload)
	if err != nil {
		return memory.RememberResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	docID := documentID(req.Record.ID)
	doc, found, err := p.getDocument(ctx, docID)
	if err != nil {
		return memory.RememberResult{}, err
	}
	state := retainedState{Replays: map[string]string{}}
	if found {
		state, err = decodeState(doc)
		if err != nil {
			return memory.RememberResult{}, err
		}
		if known, ok := state.Replays[req.ReplayKey]; req.ReplayKey != "" && ok {
			if known != digest {
				return memory.RememberResult{}, memory.ErrConflict
			}
			return p.replayResult(ctx, doc, state, req.Record)
		}
		disposition, compareErr := memory.CompareImportRevision(state.Record, req.Record)
		if compareErr != nil {
			return memory.RememberResult{}, compareErr
		}
		if disposition == memory.ImportIdempotent {
			if state.Digest != digest {
				return memory.RememberResult{}, memory.ErrConflict
			}
			return p.retainedResult(ctx, doc, state, req.Record, content)
		}
	}
	state.Record, state.Digest = req.Record, digest
	if state.Replays == nil {
		state.Replays = map[string]string{}
	}
	if req.ReplayKey != "" {
		state.Replays[req.ReplayKey] = digest
	}
	encodedState, err := encodeState(state)
	if err != nil {
		return memory.RememberResult{}, err
	}
	metadata := map[string]string{stateKey: string(encodedState), "neondoll_source_type": req.Record.Source.Type,
		"neondoll_kind": req.Record.Metadata.Kind}
	item := map[string]any{"content": content, "context": req.Record.Source.Type,
		"metadata": metadata, "document_id": docID, "tags": append([]string{"neondoll"}, req.Record.Metadata.Tags...),
		"update_mode": "replace"}
	if req.Record.ExperiencedAt != nil {
		item["timestamp"] = req.Record.ExperiencedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	var retained struct {
		Success    bool
		ItemsCount int `json:"items_count"`
	}
	if err := p.call(ctx, http.MethodPost, p.bankPath()+"/memories", nil,
		map[string]any{"items": []any{item}, "async": false}, &retained); err != nil {
		return memory.RememberResult{}, err
	}
	if !retained.Success {
		return memory.RememberResult{}, fmt.Errorf("%w: retain was unsuccessful", memory.ErrUnavailable)
	}
	doc, found, err = p.getDocument(ctx, docID)
	if err != nil {
		return memory.RememberResult{}, err
	}
	if !found {
		return memory.RememberResult{}, fmt.Errorf("%w: retained document missing", memory.ErrUnavailable)
	}
	return p.retainedResult(ctx, doc, state, req.Record, content)
}

func (p *Provider) replayResult(ctx context.Context, doc document, state retainedState, submitted memory.Record) (memory.RememberResult, error) {
	if submitted.Revision != state.Record.Revision {
		return rejected(fmt.Sprintf("replay recognized for revision %d, but retained revision %d has superseded it", submitted.Revision, state.Record.Revision)), nil
	}
	return p.retainedResult(ctx, doc, state, submitted, doc.OriginalText)
}

func (p *Provider) retainedResult(ctx context.Context, doc document, state retainedState, submitted memory.Record, submittedText string) (memory.RememberResult, error) {
	units, err := p.listDocumentMemories(ctx, doc.ID)
	if err != nil {
		return memory.RememberResult{}, err
	}
	retained, exact, err := retainedRecord(state.Record, submittedText, units)
	if err != nil {
		return memory.RememberResult{}, err
	}
	result := memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}
	if !exact {
		result.Outcome = memory.RememberTransformed
		result.Transformation = &memory.Transformation{Kind: "hindsight-native-extraction",
			Description:   "Hindsight retained extracted memory units rather than the submitted representation",
			FromMediaType: submitted.Content.MediaType, ToMediaType: retained.Content.MediaType}
	}
	return result, result.Validate()
}

func retainedRecord(base memory.Record, submittedText string, units []memoryUnit) (memory.Record, bool, error) {
	if len(units) == 1 && units[0].Text == submittedText {
		return base, true, nil
	}
	portable := make([]memoryUnit, len(units))
	for index, unit := range units {
		portable[index] = unit
		portable[index].Metadata = map[string]any{}
		for key, value := range unit.Metadata {
			if key != stateKey {
				portable[index].Metadata[key] = value
			}
		}
	}
	view := struct {
		Provider string       `json:"provider"`
		Memories []memoryUnit `json:"memories"`
	}{"hindsight", portable}
	data, err := json.Marshal(view)
	if err != nil {
		return memory.Record{}, false, err
	}
	base.Content = memory.Content{MediaType: "application/json", Transport: memory.TransportInline, Data: data}
	if base.Metadata.Extensions == nil {
		base.Metadata.Extensions = map[string]json.RawMessage{}
	}
	base.Metadata.Extensions["hindsight.retained"] = json.RawMessage(`true`)
	return base, false, base.Validate()
}

func documentID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "neondoll-" + hex.EncodeToString(sum[:])
}
func (p *Provider) getDocument(ctx context.Context, id string) (document, bool, error) {
	var doc document
	err := p.call(ctx, http.MethodGet, p.bankPath()+"/documents/"+url.PathEscape(id), nil, nil, &doc)
	if isNotFound(err) {
		return document{}, false, nil
	}
	return doc, err == nil, err
}
func decodeState(doc document) (retainedState, error) {
	raw, ok := doc.DocumentMetadata[stateKey].(string)
	if !ok {
		return retainedState{}, fmt.Errorf("%w: Hindsight document lacks NeonDoll state", memory.ErrInvalidResult)
	}
	var state retainedState
	if json.Unmarshal([]byte(raw), &state) != nil {
		return retainedState{}, fmt.Errorf("%w: malformed Hindsight document state", memory.ErrInvalidResult)
	}
	if state.Record.Content.Transport == memory.TransportInline {
		if state.Record.Content.MediaType == "text/plain" {
			state.Record.Content.Data, _ = json.Marshal(doc.OriginalText)
		} else {
			state.Record.Content.Data = json.RawMessage(doc.OriginalText)
		}
	}
	if state.Record.Validate() != nil {
		return retainedState{}, fmt.Errorf("%w: malformed Hindsight document state", memory.ErrInvalidResult)
	}
	if state.Replays == nil {
		state.Replays = map[string]string{}
	}
	return state, nil
}
func encodeState(state retainedState) ([]byte, error) {
	state.Record.Content.Data = nil
	return json.Marshal(state)
}
func (p *Provider) listDocumentMemories(ctx context.Context, documentID string) ([]memoryUnit, error) {
	query := url.Values{"document_id": {documentID}, "limit": {strconv.Itoa(p.pageSize)}, "offset": {"0"}}
	var all []memoryUnit
	for {
		var page listMemories
		if err := p.call(ctx, http.MethodGet, p.bankPath()+"/memories/list", query, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if len(all) > p.maxDocumentMemories {
			return nil, memory.ErrTransferLimit
		}
		if len(all) >= page.Total || len(page.Items) == 0 {
			return all, nil
		}
		query.Set("offset", strconv.Itoa(len(all)))
	}
}
func recordDigest(record memory.Record, payload []byte) (string, error) {
	encoded, err := memory.EncodeRecord(record)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(encoded)
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}
