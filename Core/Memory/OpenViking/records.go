package openviking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

const retainedFormat = "neondoll.memory-record.v1"

type retainedDocument struct {
	Format     string            `json:"format"`
	Record     memory.Record     `json:"record"`
	Payload    string            `json:"payload_base64,omitempty"`
	SearchText string            `json:"search_text"`
	Replays    map[string]string `json:"replays,omitempty"`
}

func (p *Provider) Remember(ctx context.Context, req memory.RememberRequest) (memory.RememberResult, error) {
	if err := ctx.Err(); err != nil {
		return memory.RememberResult{}, err
	}
	if err := req.Record.Validate(); err != nil {
		return rejected(err.Error()), nil
	}
	payload, err := p.readPayload(ctx, req.Record, req.Payload)
	if err != nil {
		if isRecordError(err) {
			return rejected(err.Error()), nil
		}
		return memory.RememberResult{}, err
	}
	digest, err := retainedDigest(req.Record, payload)
	if err != nil {
		return memory.RememberResult{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return memory.RememberResult{}, memory.ErrUnavailable
	}
	existing, found, err := p.readDocument(ctx, p.recordURI(req.Record.ID))
	if err != nil {
		return memory.RememberResult{}, err
	}
	if found {
		if old, ok := existing.Replays[req.ReplayKey]; req.ReplayKey != "" && ok {
			if old != digest {
				return memory.RememberResult{}, memory.ErrConflict
			}
			return replayResult(req.Record, payload, existing)
		}
		disposition, compareErr := memory.CompareImportRevision(existing.Record, req.Record)
		if compareErr != nil {
			return memory.RememberResult{}, compareErr
		}
		if disposition == memory.ImportIdempotent {
			if !bytes.Equal(existing.payload(), payload) {
				return memory.RememberResult{}, memory.ErrConflict
			}
			retained := existing.Record
			return memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}, nil
		}
	} else {
		existing = retainedDocument{Format: retainedFormat, Replays: map[string]string{}}
	}
	if req.ReplayKey != "" && existing.Replays[req.ReplayKey] == "" && len(existing.Replays) >= p.maxReplayKeys {
		return memory.RememberResult{}, fmt.Errorf("%w: replay-key limit", memory.ErrTransferLimit)
	}
	existing.Record, existing.Payload = req.Record, base64.StdEncoding.EncodeToString(payload)
	existing.SearchText = searchableContent(req.Record, payload)
	if req.ReplayKey != "" {
		existing.Replays[req.ReplayKey] = digest
	}
	uri := p.recordURI(req.Record.ID)
	if err := p.writeDocument(ctx, uri, existing); err != nil {
		return memory.RememberResult{}, err
	}
	retained, ok, err := p.readDocument(ctx, uri)
	if err != nil {
		return memory.RememberResult{}, err
	}
	if !ok {
		return memory.RememberResult{}, fmt.Errorf("%w: retained record disappeared", memory.ErrUnavailable)
	}
	return retainedResult(req.Record, payload, retained)
}

func replayResult(submitted memory.Record, payload []byte, retained retainedDocument) (memory.RememberResult, error) {
	if submitted.Revision != retained.Record.Revision {
		return rejected(fmt.Sprintf(
			"replay recognized for revision %d, but retained revision %d has superseded it",
			submitted.Revision, retained.Record.Revision,
		)), nil
	}
	// The replay identifies the currently retained revision. Preserve honest
	// transformation reporting if OpenViking retained a different representation.
	return retainedResult(submitted, payload, retained)
}

func retainedResult(submitted memory.Record, payload []byte, retained retainedDocument) (memory.RememberResult, error) {
	result := memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained.Record}
	if !sameRetained(submitted, payload, retained.Record, retained.payload()) {
		result.Outcome = memory.RememberTransformed
		result.Transformation = &memory.Transformation{
			Kind:          "openviking-retained-representation",
			Description:   "OpenViking currently retains a representation different from the submitted request",
			FromMediaType: submitted.Content.MediaType, ToMediaType: retained.Record.Content.MediaType,
		}
	}
	return result, result.Validate()
}

func (p *Provider) readPayload(ctx context.Context, record memory.Record, reader memory.StreamReader) ([]byte, error) {
	mediaType, _, _ := mime.ParseMediaType(record.Content.MediaType)
	if mediaType != "text/plain" && mediaType != "application/json" {
		return nil, memory.ErrUnsupportedContent
	}
	if record.Content.Transport == memory.TransportInline {
		if uint64(len(record.Content.Data)) > p.maxInline {
			return nil, memory.ErrTransferLimit
		}
		return nil, nil
	}
	if reader == nil {
		return nil, memory.ErrInvalidRequest
	}
	var out bytes.Buffer
	if _, err := memory.CopyBounded(ctx, memory.Writer(&out), reader, p.maxStream); err != nil {
		return nil, err
	}
	payload := out.Bytes()
	if mediaType == "text/plain" && !utf8.Valid(payload) {
		return nil, memory.ErrInvalidRecord
	}
	if mediaType == "application/json" && !json.Valid(payload) {
		return nil, memory.ErrInvalidRecord
	}
	return payload, nil
}

func (p *Provider) recordURI(id string) string {
	sum := sha256.Sum256([]byte(id))
	return p.root + "/" + hex.EncodeToString(sum[:]) + ".json"
}

func (p *Provider) isRecordURI(uri string) bool {
	if !strings.HasPrefix(uri, p.root+"/") {
		return false
	}
	name := strings.TrimPrefix(uri, p.root+"/")
	if strings.Contains(name, "/") || len(name) != 69 || !strings.HasSuffix(name, ".json") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".json"))
	return err == nil
}

func (p *Provider) writeDocument(ctx context.Context, uri string, doc retainedDocument) error {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return p.call(ctx, http.MethodPost, "/api/v1/content/write", nil, map[string]any{
		"uri": uri, "content": string(encoded), "mode": "replace", "wait": true,
		"tags": []string{"neondoll=doll-memory"}, "tag_mode": "replace",
	}, nil, nil)
}

func (p *Provider) readDocument(ctx context.Context, uri string) (retainedDocument, bool, error) {
	var raw string
	if err := p.call(ctx, http.MethodGet, "/api/v1/content/read", url.Values{"uri": {uri}, "raw": {"true"}}, nil, &raw, nil); err != nil {
		if isNotFound(err) {
			return retainedDocument{}, false, nil
		}
		return retainedDocument{}, false, err
	}
	var doc retainedDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || doc.Format != retainedFormat || doc.Record.Validate() != nil {
		return retainedDocument{}, false, fmt.Errorf("%w: malformed retained OpenViking record", memory.ErrInvalidResult)
	}
	if doc.Replays == nil {
		doc.Replays = map[string]string{}
	}
	if doc.Record.Content.Transport == memory.TransportStream {
		payload, err := base64.StdEncoding.DecodeString(doc.Payload)
		if err != nil {
			return retainedDocument{}, false, fmt.Errorf("%w: malformed retained payload", memory.ErrInvalidResult)
		}
		if doc.Record.Content.Size != nil && int64(len(payload)) != *doc.Record.Content.Size {
			return retainedDocument{}, false, fmt.Errorf("%w: retained payload size mismatch", memory.ErrInvalidResult)
		}
	}
	return doc, true, nil
}

func (d retainedDocument) payload() []byte {
	b, _ := base64.StdEncoding.DecodeString(d.Payload)
	return b
}
func rejected(reason string) memory.RememberResult {
	return memory.RememberResult{Outcome: memory.RememberRejected, Reason: reason}
}
func isRecordError(err error) bool {
	return errors.Is(err, memory.ErrUnsupportedContent) || errors.Is(err, memory.ErrTransferLimit) || errors.Is(err, memory.ErrInvalidRecord) || errors.Is(err, memory.ErrInvalidRequest)
}

func retainedDigest(record memory.Record, payload []byte) (string, error) {
	b, err := memory.EncodeRecord(record)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(b)
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}
func sameRetained(a memory.Record, ap []byte, b memory.Record, bp []byte) bool {
	ad, ae := retainedDigest(a, ap)
	bd, be := retainedDigest(b, bp)
	return ae == nil && be == nil && ad == bd
}
func searchableContent(record memory.Record, payload []byte) string {
	if record.Content.Transport == memory.TransportStream {
		return string(payload)
	}
	mediaType, _, _ := mime.ParseMediaType(record.Content.MediaType)
	if mediaType == "text/plain" {
		var text string
		if json.Unmarshal(record.Content.Data, &text) == nil {
			return text
		}
	}
	return string(record.Content.Data)
}
