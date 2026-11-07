package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type fakeViking struct {
	mu        sync.Mutex
	docs      map[string]string
	key       string
	transform func(retainedDocument) retainedDocument
	fail      bool
	delay     time.Duration
}

func newFake(t *testing.T) (*fakeViking, *httptest.Server) {
	t.Helper()
	f := &fakeViking{docs: map[string]string{}, key: "secret"}
	s := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(s.Close)
	return f, s
}

func (f *fakeViking) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delay, failed, key, transform := f.delay, f.fail, f.key, f.transform
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if failed {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("X-API-Key") != key {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad key")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/system/status":
		writeOK(w, map[string]any{"status": "healthy"}, false)
	case "POST /api/v1/content/write":
		var in struct{ URI, Content string }
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, 400, "INVALID", "json")
			return
		}
		if transform != nil {
			var doc retainedDocument
			if json.Unmarshal([]byte(in.Content), &doc) == nil {
				doc = transform(doc)
				b, _ := json.Marshal(doc)
				in.Content = string(b)
			}
		}
		f.mu.Lock()
		f.docs[in.URI] = in.Content
		f.mu.Unlock()
		writeOK(w, map[string]any{"uri": in.URI}, false)
	case "GET /api/v1/content/read":
		f.mu.Lock()
		content, ok := f.docs[r.URL.Query().Get("uri")]
		f.mu.Unlock()
		if !ok {
			writeErr(w, 404, "NOT_FOUND", "missing")
			return
		}
		writeOK(w, content, false)
	case "POST /api/v1/search/find":
		var in struct {
			TargetURI string `json:"target_uri"`
			Limit     int    `json:"limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		uris := keysBelow(f.docs, in.TargetURI)
		f.mu.Unlock()
		if len(uris) > in.Limit {
			uris = uris[:in.Limit]
		}
		memories := make([]map[string]any, len(uris))
		for i, uri := range uris {
			memories[i] = map[string]any{"uri": uri}
		}
		writeOK(w, map[string]any{"memories": memories, "resources": []any{}}, false)
	case "GET /api/v1/fs/ls":
		root, offset, limit := r.URL.Query().Get("uri"), atoi(r.URL.Query().Get("offset")), atoi(r.URL.Query().Get("limit"))
		f.mu.Lock()
		uris := keysBelow(f.docs, root)
		f.mu.Unlock()
		end := offset + limit
		if end > len(uris) {
			end = len(uris)
		}
		if offset > len(uris) {
			offset = len(uris)
		}
		entries := make([]map[string]any, 0, end-offset)
		for _, uri := range uris[offset:end] {
			entries = append(entries, map[string]any{"uri": uri, "isDir": false})
		}
		writeOK(w, entries, end < len(uris))
	default:
		writeErr(w, 404, "NOT_FOUND", "route")
	}
}

func writeOK(w http.ResponseWriter, result any, more bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": result, "has_more": more})
}
func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": map[string]string{"code": code, "message": message}})
}
func keysBelow(m map[string]string, root string) []string {
	out := []string{}
	for k := range m {
		if strings.HasPrefix(k, root+"/") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
func atoi(s string) int { var n int; _, _ = fmt.Sscan(s, &n); return n }

func (f *fakeViking) configure(fail bool, delay time.Duration, transform func(retainedDocument) retainedDocument) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail, f.delay, f.transform = fail, delay, transform
}

func openTest(t *testing.T, endpoint, doll string) *Provider {
	t.Helper()
	p, err := Open(Config{Endpoint: endpoint, APIKey: "secret", UserID: "user", DollID: doll, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func record(id string, revision uint64, value any) memory.Record {
	b, _ := json.Marshal(value)
	return memory.Record{SchemaVersion: 1, ID: id, Revision: revision, CreatedAt: time.Unix(1700000000, 0).UTC(), Source: memory.Source{Type: "interaction", Ref: "s1"}, Content: memory.Content{MediaType: "text/plain", Transport: memory.TransportInline, Data: b}, Metadata: memory.Metadata{Kind: "observation", Tags: []string{"test"}}}
}

func TestRememberRecallReplayRevisionAndRestart(t *testing.T) {
	_, server := newFake(t)
	p := openTest(t, server.URL, "doll-a")
	ctx := context.Background()
	first := record("m1", 1, "hello")
	got, err := p.Remember(ctx, memory.RememberRequest{Record: first, ReplayKey: "r1"})
	if err != nil || got.Outcome != memory.RememberStored {
		t.Fatalf("remember: %#v %v", got, err)
	}
	if _, err = p.Remember(ctx, memory.RememberRequest{Record: record("m1", 1, "different"), ReplayKey: "r1"}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("replay conflict: %v", err)
	}
	if _, err = p.Remember(ctx, memory.RememberRequest{Record: record("m1", 1, "old")}); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("conflicting revision accepted: %v", err)
	}
	second := record("m1", 2, "new")
	if _, err = p.Remember(ctx, memory.RememberRequest{Record: second, ReplayKey: "r2"}); err != nil {
		t.Fatal(err)
	}
	res, err := p.Recall(ctx, memory.RecallRequest{Query: "new", Limit: 10, Kinds: []string{"observation"}})
	if err != nil || len(res.Matches) != 1 || res.Matches[0].Reference.Revision != 2 {
		t.Fatalf("recall: %#v %v", res, err)
	}
	_ = p.Close()
	reopened := openTest(t, server.URL, "doll-a")
	res, err = reopened.Recall(ctx, memory.RecallRequest{Query: "new", Limit: 10})
	if err != nil || len(res.Matches) != 1 {
		t.Fatalf("restart: %#v %v", res, err)
	}
}

func TestStreamExportImportAndIsolation(t *testing.T) {
	_, server := newFake(t)
	source := openTest(t, server.URL, "a")
	payload := []byte(`{"snowman":"☃"}`)
	size := int64(len(payload))
	streamed := record("stream", 1, nil)
	streamed.Content = memory.Content{MediaType: "application/json", Transport: memory.TransportStream, Size: &size}
	if _, err := source.Remember(context.Background(), memory.RememberRequest{Record: streamed, Payload: memory.Reader(bytes.NewReader(payload))}); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	report, err := source.Export(context.Background(), memory.ExportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, sink)
	if err != nil || !report.Completed || len(sink.transfers) != 1 || !bytes.Equal(sink.payloads[0], payload) {
		t.Fatalf("export: %#v %v payload=%q", report, err, sink.payloads)
	}
	target := openTest(t, server.URL, "b")
	imported, err := target.Import(context.Background(), memory.ImportRequest{MaxRecords: 10, MaxBytes: 1 << 20}, &sliceSource{transfers: sink.transfers, payloads: sink.payloads})
	if err != nil || !imported.Completed {
		t.Fatalf("import: %#v %v", imported, err)
	}
	other, err := target.Recall(context.Background(), memory.RecallRequest{Query: "snowman", Limit: 10})
	if err != nil || len(other.Matches) != 1 {
		t.Fatalf("target recall: %#v %v", other, err)
	}
	original, err := source.Recall(context.Background(), memory.RecallRequest{Query: "snowman", Limit: 10})
	if err != nil || len(original.Matches) != 1 {
		t.Fatalf("source recall: %#v %v", original, err)
	}
}

func TestTransformationAndRejection(t *testing.T) {
	f, server := newFake(t)
	f.configure(false, 0, func(d retainedDocument) retainedDocument { d.Record.Metadata.Kind = "summary"; return d })
	p := openTest(t, server.URL, "a")
	got, err := p.Remember(context.Background(), memory.RememberRequest{Record: record("m", 1, "image description")})
	if err != nil || got.Outcome != memory.RememberTransformed || got.Transformation == nil || got.Retained.Metadata.Kind != "summary" {
		t.Fatalf("transform: %#v %v", got, err)
	}
	bad := record("bad", 1, "x")
	bad.Content.MediaType = "image/png"
	got, err = p.Remember(context.Background(), memory.RememberRequest{Record: bad})
	if err != nil || got.Outcome != memory.RememberRejected {
		t.Fatalf("reject: %#v %v", got, err)
	}
}

func TestBoundsCancellationFailuresHealthAndClose(t *testing.T) {
	f, server := newFake(t)
	p, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: "d", MaxStreamBytes: 4, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s := record("s", 1, nil)
	n := int64(5)
	s.Content = memory.Content{MediaType: "text/plain", Transport: memory.TransportStream, Size: &n}
	got, err := p.Remember(context.Background(), memory.RememberRequest{Record: s, Payload: memory.Reader(strings.NewReader("12345"))})
	if err != nil || got.Outcome != memory.RememberRejected {
		t.Fatalf("limit: %#v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = p.Recall(ctx, memory.RecallRequest{Query: "x", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	f.configure(true, 0, nil)
	health, err := p.Health(context.Background())
	if err == nil || health.Status != memory.HealthUnavailable {
		t.Fatalf("health: %#v %v", health, err)
	}
	f.configure(false, 100*time.Millisecond, nil)
	if _, err = p.Health(context.Background()); err == nil {
		t.Fatal("timeout accepted")
	}
	f.configure(false, 0, nil)
	_ = p.Close()
	if _, err = p.Remember(context.Background(), memory.RememberRequest{Record: record("x", 1, "x")}); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("closed: %v", err)
	}
}

func TestAuthenticationMalformedResponseAndExportBudget(t *testing.T) {
	f, server := newFake(t)
	wrong, err := Open(Config{Endpoint: server.URL, APIKey: "wrong", UserID: "u", DollID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Health(context.Background()); err == nil {
		t.Fatal("authentication accepted")
	}
	p := openTest(t, server.URL, "a")
	for i := 0; i < 3; i++ {
		if _, err = p.Remember(context.Background(), memory.RememberRequest{Record: record(fmt.Sprintf("m%d", i), 1, "text")}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := p.Export(context.Background(), memory.ExportRequest{MaxRecords: 1, MaxBytes: 1 << 20}, &collectSink{})
	if !errors.Is(err, memory.ErrTransferLimit) || report.Completed {
		t.Fatalf("bounded export: %#v %v", report, err)
	}
	f.mu.Lock()
	for k := range f.docs {
		f.docs[k] = "not-json"
		break
	}
	f.mu.Unlock()
	if _, err = p.Recall(context.Background(), memory.RecallRequest{Query: "x", Limit: 10}); !errors.Is(err, memory.ErrInvalidResult) {
		t.Fatalf("malformed: %v", err)
	}
}

type collectSink struct {
	transfers []memory.Transfer
	payloads  [][]byte
}

func (s *collectSink) Put(ctx context.Context, t memory.Transfer) error {
	var b bytes.Buffer
	if t.Payload != nil {
		if _, err := memory.CopyBounded(ctx, memory.Writer(&b), t.Payload, 1<<20); err != nil {
			return err
		}
	}
	s.transfers = append(s.transfers, memory.Transfer{Record: t.Record})
	s.payloads = append(s.payloads, append([]byte(nil), b.Bytes()...))
	return nil
}

type sliceSource struct {
	transfers []memory.Transfer
	payloads  [][]byte
	i         int
}

func (s *sliceSource) Next(context.Context) (memory.Transfer, error) {
	if s.i == len(s.transfers) {
		return memory.Transfer{}, io.EOF
	}
	t := s.transfers[s.i]
	if t.Record.Content.Transport == memory.TransportStream {
		t.Payload = memory.Reader(bytes.NewReader(s.payloads[s.i]))
	}
	s.i++
	return t, nil
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{{}, {Endpoint: "ftp://x", UserID: "u", DollID: "d"}, {Endpoint: "http://x", UserID: "../u", DollID: "d"}} {
		if _, err := Open(cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
	_, server := newFake(t)
	p := openTest(t, server.URL, "d")
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ContractVersion != 1 || caps.SupportsConsolidation || !caps.SupportsUpdates {
		t.Fatalf("caps: %#v %v", caps, err)
	}
}

func TestReplayMetadataIsBounded(t *testing.T) {
	_, server := newFake(t)
	p, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: "d", MaxReplayKeys: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Remember(context.Background(), memory.RememberRequest{Record: record("m", 1, "one"), ReplayKey: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Remember(context.Background(), memory.RememberRequest{Record: record("m", 2, "two"), ReplayKey: "two"}); !errors.Is(err, memory.ErrTransferLimit) {
		t.Fatalf("unbounded replay metadata: %v", err)
	}
	got, err := p.Recall(context.Background(), memory.RecallRequest{Query: "one", Limit: 1})
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Reference.Revision != 1 {
		t.Fatalf("failed write changed retained record: %#v %v", got, err)
	}
}

var _ = url.Values{}
