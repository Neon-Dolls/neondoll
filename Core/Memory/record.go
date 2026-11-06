// Package memory defines Core's portable, provider-independent retained
// memory contract. It owns no retained-memory persistence.
package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
	"unicode/utf8"
)

const SchemaVersion = 1

type Transport string

const (
	TransportInline Transport = "inline"
	TransportStream Transport = "stream"
)

var ErrInvalidRecord = errors.New("memory: invalid record")

type Source struct {
	Type string `json:"type"`
	Ref  string `json:"ref,omitempty"`
}

type Content struct {
	MediaType string          `json:"media_type"`
	Transport Transport       `json:"transport"`
	Data      json.RawMessage `json:"data,omitempty"`
	Size      *int64          `json:"size,omitempty"`
}

type Metadata struct {
	Kind       string                     `json:"kind"`
	Subject    string                     `json:"subject,omitempty"`
	Tags       []string                   `json:"tags,omitempty"`
	Confidence *float64                   `json:"confidence,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// Record is MemoryRecord v1. ExperiencedAt is intentionally not omitted: a
// JSON null is the normative representation of an unknown subjective anchor.
type Record struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Revision      uint64     `json:"revision"`
	CreatedAt     time.Time  `json:"created_at"`
	ExperiencedAt *time.Time `json:"experienced_at"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	DerivedFrom   []string   `json:"derived_from,omitempty"`
	Source        Source     `json:"source"`
	Content       Content    `json:"content"`
	Metadata      Metadata   `json:"metadata"`
}

type Reference struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

func (r Record) Reference() Reference { return Reference{ID: r.ID, Revision: r.Revision} }

func (r Record) Validate() error {
	if r.SchemaVersion != SchemaVersion || strings.TrimSpace(r.ID) == "" || r.Revision == 0 || r.CreatedAt.IsZero() {
		return ErrInvalidRecord
	}
	if r.UpdatedAt != nil && r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("%w: updated_at precedes created_at", ErrInvalidRecord)
	}
	if err := r.Source.validate(); err != nil {
		return err
	}
	if err := r.Content.validate(); err != nil {
		return err
	}
	if err := r.Metadata.validate(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.DerivedFrom))
	for _, id := range r.DerivedFrom {
		id = strings.TrimSpace(id)
		if id == "" || id == r.ID {
			return fmt.Errorf("%w: invalid derived_from", ErrInvalidRecord)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate derived_from", ErrInvalidRecord)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (s Source) validate() error {
	if strings.TrimSpace(s.Type) == "" {
		return fmt.Errorf("%w: source.type is required", ErrInvalidRecord)
	}
	if s.Type == "unknown" && strings.TrimSpace(s.Ref) != "" {
		return fmt.Errorf("%w: unknown source cannot fabricate a ref", ErrInvalidRecord)
	}
	return nil
}

func (c Content) validate() error {
	mediaType, _, err := mime.ParseMediaType(c.MediaType)
	if err != nil || strings.TrimSpace(c.MediaType) == "" {
		return fmt.Errorf("%w: invalid content media_type", ErrInvalidRecord)
	}
	if c.Size != nil && *c.Size < 0 {
		return fmt.Errorf("%w: negative content size", ErrInvalidRecord)
	}
	switch c.Transport {
	case TransportStream:
		if len(c.Data) != 0 {
			return fmt.Errorf("%w: stream content cannot contain data", ErrInvalidRecord)
		}
	case TransportInline:
		if len(c.Data) == 0 || !json.Valid(c.Data) {
			return fmt.Errorf("%w: inline data is required", ErrInvalidRecord)
		}
		switch {
		case strings.HasPrefix(mediaType, "text/"):
			var value string
			if err := json.Unmarshal(c.Data, &value); err != nil || !utf8.ValidString(value) {
				return fmt.Errorf("%w: inline text must be a UTF-8 JSON string", ErrInvalidRecord)
			}
		case mediaType == "application/json":
		default:
			return fmt.Errorf("%w: media type %q requires stream transport", ErrInvalidRecord, mediaType)
		}
	default:
		return fmt.Errorf("%w: unsupported transport %q", ErrInvalidRecord, c.Transport)
	}
	return nil
}

func (m Metadata) validate() error {
	if strings.TrimSpace(m.Kind) == "" {
		return fmt.Errorf("%w: metadata.kind is required", ErrInvalidRecord)
	}
	if m.Subject != "" && strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("%w: metadata.subject is empty", ErrInvalidRecord)
	}
	if m.Confidence != nil && (*m.Confidence < 0 || *m.Confidence > 1) {
		return fmt.Errorf("%w: confidence outside 0..1", ErrInvalidRecord)
	}
	for _, tag := range m.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("%w: empty metadata tag", ErrInvalidRecord)
		}
	}
	for name, value := range m.Extensions {
		if strings.TrimSpace(name) == "" || !json.Valid(value) {
			return fmt.Errorf("%w: invalid metadata extension", ErrInvalidRecord)
		}
	}
	return nil
}

func EncodeRecord(record Record) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(record)
}

func DecodeRecord(data []byte) (Record, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	for _, name := range []string{"schema_version", "id", "revision", "created_at", "experienced_at", "source", "content", "metadata"} {
		if _, ok := fields[name]; !ok {
			return Record{}, fmt.Errorf("%w: missing %s", ErrInvalidRecord, name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Record{}, fmt.Errorf("%w: trailing JSON", ErrInvalidRecord)
	}
	return record, nil
}
