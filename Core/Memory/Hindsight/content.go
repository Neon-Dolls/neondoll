package hindsight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"unicode/utf8"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func (p *Provider) readContent(ctx context.Context, record memory.Record, reader memory.StreamReader) (string, []byte, error) {
	mediaType, _, _ := mime.ParseMediaType(record.Content.MediaType)
	if mediaType != "text/plain" && mediaType != "application/json" {
		return "", nil, memory.ErrUnsupportedContent
	}
	var payload []byte
	if record.Content.Transport == memory.TransportInline {
		if uint64(len(record.Content.Data)) > p.maxInline {
			return "", nil, memory.ErrTransferLimit
		}
		payload = append([]byte(nil), record.Content.Data...)
		if mediaType == "text/plain" {
			var text string
			if json.Unmarshal(payload, &text) != nil {
				return "", nil, memory.ErrInvalidRecord
			}
			return text, payload, nil
		}
		return string(payload), payload, nil
	}
	if reader == nil {
		return "", nil, memory.ErrInvalidRequest
	}
	var out bytes.Buffer
	if _, err := memory.CopyBounded(ctx, memory.Writer(&out), reader, p.maxStream); err != nil {
		return "", nil, err
	}
	payload = out.Bytes()
	if record.Content.Size != nil && *record.Content.Size != int64(len(payload)) {
		return "", nil, memory.ErrInvalidRecord
	}
	if !utf8.Valid(payload) || (mediaType == "application/json" && !json.Valid(payload)) {
		return "", nil, memory.ErrInvalidRecord
	}
	return string(payload), payload, nil
}

func rejected(reason string) memory.RememberResult {
	return memory.RememberResult{Outcome: memory.RememberRejected, Reason: reason}
}

func recordError(err error) bool {
	return errors.Is(err, memory.ErrUnsupportedContent) || errors.Is(err, memory.ErrTransferLimit) ||
		errors.Is(err, memory.ErrInvalidRecord) || errors.Is(err, memory.ErrInvalidRequest)
}
