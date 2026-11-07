// Package openviking implements the Core Memory Provider contract using the
// documented OpenViking HTTP API. OpenViking owns the retained representation;
// Core does not keep a parallel memory journal.
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
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

const (
	APIVersion                      = "v1"
	TargetOpenVikingRevision        = "9ac008f9b86c91b79d6e93eb8df8ea7f4d48cecb"
	DefaultTimeout                  = 30 * time.Second
	DefaultMaxInlineBytes    uint64 = 4 << 20
	DefaultMaxStreamBytes    uint64 = 16 << 20
	DefaultMaxReplayKeys            = 1024
	retainedFormat                  = "neondoll.memory-record.v1"
)

type Config struct {
	Endpoint       string
	APIKey         string
	UserID         string
	DollID         string
	Namespace      string
	Timeout        time.Duration
	MaxInlineBytes uint64
	MaxStreamBytes uint64
	MaxReplayKeys  int
	HTTPClient     *http.Client
}

type Provider struct {
	endpoint, apiKey, root string
	client                 *http.Client
	maxInline, maxStream   uint64
	maxReplayKeys          int
	timeout                time.Duration
	mu                     sync.Mutex
	closeOnce              sync.Once
	closed                 atomic.Bool
}

type retainedDocument struct {
	Format     string            `json:"format"`
	Record     memory.Record     `json:"record"`
	Payload    string            `json:"payload_base64,omitempty"`
	SearchText string            `json:"search_text"`
	Replays    map[string]string `json:"replays,omitempty"`
}

type envelope[T any] struct {
	Status  string `json:"status"`
	Result  T      `json:"result"`
	HasMore bool   `json:"has_more,omitempty"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var _ memory.Provider = (*Provider)(nil)

func Open(cfg Config) (*Provider, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if cfg.Endpoint == "" || strings.TrimSpace(cfg.UserID) == "" || strings.TrimSpace(cfg.DollID) == "" {
		return nil, fmt.Errorf("%w: endpoint, user ID, and doll ID are required", memory.ErrInvalidRequest)
	}
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid OpenViking endpoint", memory.ErrInvalidRequest)
	}
	if !safeSegment(cfg.UserID) || !safeSegment(cfg.DollID) || (cfg.Namespace != "" && !safeSegment(cfg.Namespace)) {
		return nil, fmt.Errorf("%w: invalid namespace component", memory.ErrInvalidRequest)
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "neondoll"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxInlineBytes == 0 {
		cfg.MaxInlineBytes = DefaultMaxInlineBytes
	}
	if cfg.MaxStreamBytes == 0 {
		cfg.MaxStreamBytes = DefaultMaxStreamBytes
	}
	if cfg.MaxReplayKeys == 0 {
		cfg.MaxReplayKeys = DefaultMaxReplayKeys
	}
	if cfg.MaxReplayKeys < 0 {
		return nil, fmt.Errorf("%w: negative replay-key limit", memory.ErrInvalidRequest)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{
		endpoint: cfg.Endpoint, apiKey: cfg.APIKey, client: client,
		root:      fmt.Sprintf("viking://user/%s/memories/%s/%s", cfg.UserID, cfg.Namespace, cfg.DollID),
		maxInline: cfg.MaxInlineBytes, maxStream: cfg.MaxStreamBytes, maxReplayKeys: cfg.MaxReplayKeys,
		timeout: cfg.Timeout,
	}, nil
}

func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#") && strings.TrimSpace(s) == s
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
		if errors.Is(err, memory.ErrUnsupportedContent) || errors.Is(err, memory.ErrTransferLimit) || errors.Is(err, memory.ErrInvalidRecord) {
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
		if req.ReplayKey != "" {
			if old, ok := existing.Replays[req.ReplayKey]; ok {
				if old != digest {
					return memory.RememberResult{}, memory.ErrConflict
				}
				retained := existing.Record
				result := memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}
				if !sameRetained(req.Record, payload, retained, existing.payload()) {
					result.Outcome = memory.RememberTransformed
					result.Transformation = &memory.Transformation{Kind: "openviking-retained-representation", Description: "OpenViking retained a representation different from the submitted record", FromMediaType: req.Record.Content.MediaType, ToMediaType: retained.Content.MediaType}
				}
				return result, result.Validate()
			}
		}
		disposition, compareErr := memory.CompareImportRevision(existing.Record, req.Record)
		if compareErr != nil {
			return memory.RememberResult{}, compareErr
		}
		if disposition == memory.ImportIdempotent && bytes.Equal(existing.payload(), payload) {
			retained := existing.Record
			return memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}, nil
		}
		if disposition == memory.ImportIdempotent {
			return memory.RememberResult{}, memory.ErrConflict
		}
	} else {
		existing = retainedDocument{Format: retainedFormat, Replays: map[string]string{}}
	}
	existing.Record = req.Record
	existing.Payload = base64.StdEncoding.EncodeToString(payload)
	existing.SearchText = searchableContent(req.Record, payload)
	if req.ReplayKey != "" && existing.Replays[req.ReplayKey] == "" && len(existing.Replays) >= p.maxReplayKeys {
		return memory.RememberResult{}, fmt.Errorf("%w: replay-key limit", memory.ErrTransferLimit)
	}
	if req.ReplayKey != "" {
		existing.Replays[req.ReplayKey] = digest
	}
	if err := p.writeDocument(ctx, p.recordURI(req.Record.ID), existing); err != nil {
		return memory.RememberResult{}, err
	}
	retained, ok, err := p.readDocument(ctx, p.recordURI(req.Record.ID))
	if err != nil {
		return memory.RememberResult{}, err
	}
	if !ok {
		return memory.RememberResult{}, fmt.Errorf("%w: retained record disappeared", memory.ErrUnavailable)
	}
	result := memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained.Record}
	if !sameRetained(req.Record, payload, retained.Record, retained.payload()) {
		result.Outcome = memory.RememberTransformed
		result.Transformation = &memory.Transformation{Kind: "openviking-retained-representation", Description: "OpenViking retained a representation different from the submitted record", FromMediaType: req.Record.Content.MediaType, ToMediaType: retained.Record.Content.MediaType}
	}
	return result, result.Validate()
}

func (p *Provider) Recall(ctx context.Context, req memory.RecallRequest) (memory.RecallResult, error) {
	if err := req.Validate(); err != nil {
		return memory.RecallResult{}, err
	}
	var result struct {
		Memories []struct {
			URI string `json:"uri"`
		} `json:"memories"`
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	query := req.Query
	if strings.TrimSpace(query) == "" {
		query = "memory"
	}
	body := map[string]any{"query": query, "target_uri": p.root, "limit": int(req.Limit), "context_type": []string{"memory"}}
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
	out := memory.RecallResult{}
	seen := map[string]bool{}
	for _, uri := range uris {
		if len(out.Matches) == int(req.Limit) {
			break
		}
		if seen[uri] || !strings.HasPrefix(uri, p.root+"/") {
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

func (p *Provider) Export(ctx context.Context, req memory.ExportRequest, sink memory.ExportSink) (memory.ExportReport, error) {
	bounded, err := memory.NewBoundedExportSink(req, sink)
	if err != nil {
		return memory.ExportReport{}, err
	}
	report := memory.ExportReport{Completed: true}
	offset := 0
	for {
		var entries []struct {
			URI   string `json:"uri"`
			IsDir bool   `json:"isDir"`
		}
		q := url.Values{"uri": {p.root}, "offset": {fmt.Sprint(offset)}, "limit": {"100"}, "sort_by": {"name"}, "sort_order": {"asc"}}
		var more bool
		if err := p.call(ctx, http.MethodGet, "/api/v1/fs/ls", q, nil, &entries, &more); err != nil {
			if isNotFound(err) {
				return report, nil
			}
			return report, err
		}
		for _, entry := range entries {
			if entry.IsDir || !strings.HasSuffix(entry.URI, ".json") {
				continue
			}
			doc, ok, err := p.readDocument(ctx, entry.URI)
			if err != nil {
				report.Completed = false
				return report, err
			}
			if !ok {
				continue
			}
			transfer := memory.Transfer{Record: doc.Record}
			payload := doc.payload()
			if doc.Record.Content.Transport == memory.TransportStream {
				transfer.Payload = memory.Reader(bytes.NewReader(payload))
			}
			if err := bounded.Put(ctx, transfer); err != nil {
				report.Completed = false
				report.Failures = append(report.Failures, memory.TransferFailure{Reference: doc.Record.Reference(), Reason: err.Error()})
				report.Records, report.Bytes = bounded.Usage()
				return report, err
			}
		}
		if !more || len(entries) == 0 {
			break
		}
		offset += len(entries)
	}
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func (p *Provider) Import(ctx context.Context, req memory.ImportRequest, source memory.ImportSource) (memory.ImportReport, error) {
	bounded, err := memory.NewBoundedImportSource(req, source)
	if err != nil {
		return memory.ImportReport{}, err
	}
	report := memory.ImportReport{Completed: true}
	for {
		transfer, err := bounded.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.Completed = false
			report.Records, report.Bytes = bounded.Usage()
			return report, err
		}
		result, rememberErr := p.Remember(ctx, memory.RememberRequest{Record: transfer.Record, Payload: transfer.Payload})
		outcome := memory.ImportOutcome{Source: transfer.Record.Reference()}
		if rememberErr != nil {
			outcome.Disposition, outcome.Reason = memory.ImportRejected, rememberErr.Error()
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		if result.Outcome == memory.RememberRejected {
			outcome.Disposition, outcome.Reason = memory.ImportRejected, result.Reason
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		retainedReference := result.Retained.Reference()
		outcome.Retained = &retainedReference
		switch result.Outcome {
		case memory.RememberTransformed:
			outcome.Disposition = memory.ImportTransformed
		default:
			outcome.Disposition = memory.ImportStored
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	report.Records, report.Bytes = bounded.Usage()
	return report, nil
}

func (p *Provider) Capabilities(context.Context) (memory.Capabilities, error) {
	return memory.Capabilities{ContractVersion: memory.ContractVersion, ContentTypes: []string{"text/plain", "application/json"}, MaxInlineBytes: p.maxInline, MaxStreamBytes: p.maxStream, SupportsUpdates: true, SupportsConsolidation: false}, nil
}

func (p *Provider) Health(ctx context.Context) (memory.Health, error) {
	var result any
	if err := p.call(ctx, http.MethodGet, "/api/v1/system/status", nil, nil, &result, nil); err != nil {
		return memory.Health{Status: memory.HealthUnavailable, Detail: "OpenViking API unavailable"}, err
	}
	return memory.Health{Status: memory.HealthHealthy, Detail: "OpenViking API reachable"}, nil
}

func (p *Provider) Close() error {
	p.closeOnce.Do(func() { p.closed.Store(true); p.client.CloseIdleConnections() })
	return nil
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

func (p *Provider) writeDocument(ctx context.Context, uri string, doc retainedDocument) error {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return p.call(ctx, http.MethodPost, "/api/v1/content/write", nil, map[string]any{"uri": uri, "content": string(encoded), "mode": "replace", "wait": true, "tags": []string{"neondoll=doll-memory"}, "tag_mode": "replace"}, nil, nil)
}

func (p *Provider) readDocument(ctx context.Context, uri string) (retainedDocument, bool, error) {
	var raw string
	q := url.Values{"uri": {uri}, "raw": {"true"}}
	if err := p.call(ctx, http.MethodGet, "/api/v1/content/read", q, nil, &raw, nil); err != nil {
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

func (p *Provider) call(ctx context.Context, method, route string, query url.Values, body any, result any, hasMore *bool) error {
	if p.closed.Load() {
		return memory.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, p.timeout)
	defer cancel()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	u := p.endpoint + route
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		req.Header.Set("X-API-Key", p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", memory.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.maxStream)+1))
	if err != nil {
		return fmt.Errorf("%w: %v", memory.ErrUnavailable, err)
	}
	if uint64(len(data)) > p.maxStream {
		return memory.ErrTransferLimit
	}
	var env envelope[json.RawMessage]
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%w: malformed OpenViking response", memory.ErrInvalidResult)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Status != "ok" {
		code, message := "", http.StatusText(resp.StatusCode)
		if env.Error != nil {
			code, message = env.Error.Code, env.Error.Message
		}
		return &apiError{status: resp.StatusCode, code: code, message: message}
	}
	if hasMore != nil {
		*hasMore = env.HasMore
	}
	if result != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("%w: malformed OpenViking result", memory.ErrInvalidResult)
		}
	}
	return nil
}

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openviking: HTTP %d %s: %s", e.status, e.code, e.message)
}
func isNotFound(err error) bool {
	var target *apiError
	return errors.As(err, &target) && target.status == http.StatusNotFound
}
func rejected(reason string) memory.RememberResult {
	return memory.RememberResult{Outcome: memory.RememberRejected, Reason: reason}
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
func matches(record memory.Record, req memory.RecallRequest) bool {
	if len(req.Kinds) > 0 {
		ok := false
		for _, kind := range req.Kinds {
			if record.Metadata.Kind == kind {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if len(req.SourceTypes) > 0 {
		ok := false
		for _, source := range req.SourceTypes {
			if record.Source.Type == source {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
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
