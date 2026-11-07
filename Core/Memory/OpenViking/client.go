package openviking

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

type envelope[T any] struct {
	Status  string `json:"status"`
	Result  T      `json:"result"`
	HasMore bool   `json:"has_more,omitempty"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *Provider) call(ctx context.Context, method, route string, query url.Values, body, result any, hasMore *bool) error {
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
