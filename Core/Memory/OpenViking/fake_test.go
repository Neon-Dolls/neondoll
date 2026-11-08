package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type fakeViking struct {
	mu                      sync.Mutex
	docs                    map[string]string
	key                     string
	transform               func(retainedDocument) retainedDocument
	failStatus              int
	delay                   time.Duration
	writes, failWritesAfter int
	searchOrder             []string
	lastSearchLimit         int
	malformed               bool
	failReadURI             string
}

func newFake(t *testing.T) (*fakeViking, *httptest.Server) {
	t.Helper()
	f := &fakeViking{docs: map[string]string{}, key: "secret", failWritesAfter: -1}
	s := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(s.Close)
	return f, s
}

func (f *fakeViking) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delay, status, key, transform, malformed := f.delay, f.failStatus, f.key, f.transform, f.malformed
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		writeErr(w, status, "UNAVAILABLE", "injected failure")
		return
	}
	if malformed {
		_, _ = w.Write([]byte(`{"status":`))
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
		f.write(w, r, transform)
	case "GET /api/v1/content/read":
		f.read(w, r)
	case "POST /api/v1/search/find":
		f.search(w, r)
	case "GET /api/v1/fs/ls":
		f.list(w, r)
	default:
		writeErr(w, 404, "NOT_FOUND", "route")
	}
}

func (f *fakeViking) write(w http.ResponseWriter, r *http.Request, transform func(retainedDocument) retainedDocument) {
	var in struct{ URI, Content string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "INVALID", "json")
		return
	}
	f.mu.Lock()
	if f.failWritesAfter >= 0 && f.writes >= f.failWritesAfter {
		f.mu.Unlock()
		writeErr(w, 503, "UNAVAILABLE", "write failed")
		return
	}
	f.writes++
	f.mu.Unlock()
	if transform != nil {
		var d retainedDocument
		if json.Unmarshal([]byte(in.Content), &d) == nil {
			d = transform(d)
			b, _ := json.Marshal(d)
			in.Content = string(b)
		}
	}
	f.mu.Lock()
	f.docs[in.URI] = in.Content
	f.mu.Unlock()
	writeOK(w, map[string]any{"uri": in.URI}, false)
}

func (f *fakeViking) read(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	f.mu.Lock()
	content, ok := f.docs[uri]
	fail := f.failReadURI == uri
	f.mu.Unlock()
	if fail {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "read failed")
		return
	}
	if !ok {
		writeErr(w, 404, "NOT_FOUND", "missing")
		return
	}
	writeOK(w, content, false)
}

func (f *fakeViking) search(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetURI string `json:"target_uri"`
		Limit     int    `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.lastSearchLimit = in.Limit
	uris := append([]string(nil), f.searchOrder...)
	if len(uris) == 0 {
		uris = keysBelow(f.docs, in.TargetURI)
	}
	f.mu.Unlock()
	if len(uris) > in.Limit {
		uris = uris[:in.Limit]
	}
	hits := make([]map[string]any, len(uris))
	for i, uri := range uris {
		hits[i] = map[string]any{"uri": uri}
	}
	writeOK(w, map[string]any{"memories": hits, "resources": []any{}}, false)
}

func (f *fakeViking) list(w http.ResponseWriter, r *http.Request) {
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
