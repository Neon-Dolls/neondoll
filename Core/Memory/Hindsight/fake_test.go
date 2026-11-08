package hindsight

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fakeHindsight struct {
	mu                   sync.Mutex
	token                string
	server               *httptest.Server
	docs                 map[string]map[string]document
	units                map[string]map[string][]memoryUnit
	failRetain, failList bool
	failRetainAfter      int
	retainCount          int
	malformedRecall      bool
	delay                time.Duration
}

func newFakeHindsight(token string) *fakeHindsight {
	f := &fakeHindsight{token: token, docs: map[string]map[string]document{}, units: map[string]map[string][]memoryUnit{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	return f
}
func (f *fakeHindsight) close() { f.server.Close() }

func (f *fakeHindsight) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || strings.Join(parts[:3], "/") != "v1/default/banks" {
		http.NotFound(w, r)
		return
	}
	bank := parts[3]
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(parts) == 4 && r.Method == http.MethodGet {
		f.write(w, map[string]any{"bank_id": bank})
		return
	}
	if len(parts) >= 5 && parts[4] == "memories" {
		f.memories(w, r, bank, parts[5:])
		return
	}
	if len(parts) >= 5 && parts[4] == "documents" {
		f.documents(w, r, bank, parts[5:])
		return
	}
	http.NotFound(w, r)
}

func (f *fakeHindsight) memories(w http.ResponseWriter, r *http.Request, bank string, rest []string) {
	if len(rest) == 0 && r.Method == http.MethodPost {
		if f.failRetain || (f.failRetainAfter > 0 && f.retainCount >= f.failRetainAfter) {
			http.Error(w, `{"detail":"storage unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		var request struct {
			Items []struct {
				Content    string            `json:"content"`
				Context    string            `json:"context"`
				DocumentID string            `json:"document_id"`
				Metadata   map[string]string `json:"metadata"`
				Tags       []string          `json:"tags"`
			} `json:"items"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Items) != 1 {
			http.Error(w, `{}`, 422)
			return
		}
		item := request.Items[0]
		if f.docs[bank] == nil {
			f.docs[bank] = map[string]document{}
			f.units[bank] = map[string][]memoryUnit{}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		metadata := map[string]any{}
		for key, value := range item.Metadata {
			metadata[key] = value
		}
		f.docs[bank][item.DocumentID] = document{ID: item.DocumentID, BankID: bank, OriginalText: item.Content,
			CreatedAt: now, UpdatedAt: now, DocumentMetadata: metadata}
		texts := []string{item.Content}
		if strings.HasPrefix(item.Content, "transform:") {
			texts = []string{"extracted one", "extracted two"}
		}
		units := make([]memoryUnit, 0, len(texts))
		for index, text := range texts {
			units = append(units, memoryUnit{ID: fmt.Sprintf("%s-%d", item.DocumentID, index), Text: text,
				Context: item.Context, FactType: "experience", DocumentID: item.DocumentID, MentionedAt: now,
				Tags: item.Tags, Metadata: metadata})
		}
		f.units[bank][item.DocumentID] = units
		doc := f.docs[bank][item.DocumentID]
		doc.MemoryUnitCount = len(units)
		f.docs[bank][item.DocumentID] = doc
		f.retainCount++
		f.write(w, map[string]any{"success": true, "bank_id": bank, "items_count": 1, "async": false})
		return
	}
	if len(rest) == 1 && rest[0] == "list" && r.Method == http.MethodGet {
		if f.failList {
			http.Error(w, `{"detail":"read failed"}`, http.StatusServiceUnavailable)
			return
		}
		var items []memoryUnit
		if id := r.URL.Query().Get("document_id"); id != "" {
			items = append(items, f.units[bank][id]...)
		} else {
			for _, group := range f.units[bank] {
				items = append(items, group...)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		f.page(w, items, r)
		return
	}
	if len(rest) == 1 && rest[0] == "recall" && r.Method == http.MethodPost {
		if f.malformedRecall {
			_, _ = w.Write([]byte(`{"results":`))
			return
		}
		var items []memoryUnit
		for _, group := range f.units[bank] {
			items = append(items, group...)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		results := make([]map[string]any, 0, len(items))
		for _, item := range items {
			metadata := map[string]string{}
			for key, value := range item.Metadata {
				metadata[key] = fmt.Sprint(value)
			}
			results = append(results, map[string]any{
				"id": item.ID, "text": item.Text, "type": item.FactType, "context": item.Context,
				"document_id": item.DocumentID, "mentioned_at": item.MentionedAt, "metadata": metadata, "tags": item.Tags})
		}
		f.write(w, map[string]any{"results": results})
		return
	}
	http.NotFound(w, r)
}

func (f *fakeHindsight) documents(w http.ResponseWriter, r *http.Request, bank string, rest []string) {
	if len(rest) == 1 && r.Method == http.MethodGet {
		doc, ok := f.docs[bank][rest[0]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.write(w, doc)
		return
	}
	if len(rest) == 0 && r.Method == http.MethodGet {
		ids := make([]string, 0, len(f.docs[bank]))
		for id := range f.docs[bank] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		items := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			items = append(items, map[string]string{"id": id})
		}
		f.page(w, items, r)
		return
	}
	http.NotFound(w, r)
}

func (f *fakeHindsight) page(w http.ResponseWriter, all any, r *http.Request) {
	raw, _ := json.Marshal(all)
	var values []json.RawMessage
	_ = json.Unmarshal(raw, &values)
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	if offset > len(values) {
		offset = len(values)
	}
	f.write(w, map[string]any{"items": values[offset:end], "total": len(values), "limit": limit, "offset": offset})
}
func (f *fakeHindsight) write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
