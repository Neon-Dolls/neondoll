package hindsight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("hindsight: HTTP %d: %s", e.status, e.body) }

func (p *Provider) call(ctx context.Context, method, path string, query url.Values, body, result any) error {
	if p.closed.Load() {
		return memory.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	u := p.endpoint + path
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
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{status: resp.StatusCode, body: string(data)}
	}
	if result != nil {
		if len(data) == 0 || json.Unmarshal(data, result) != nil {
			return fmt.Errorf("%w: malformed Hindsight response", memory.ErrInvalidResult)
		}
	}
	return nil
}

func isNotFound(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.status == http.StatusNotFound
}
