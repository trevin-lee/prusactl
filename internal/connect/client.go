// Package connect is a client for the Prusa Connect web API
// (https://connect.prusa3d.com/app/...), the same API the Connect web app uses.
// Prusa does not publish it; paths and payloads here were read from the web
// app and checked against live responses.
package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/trevin-lee/prusactl/internal/redact"
)

// DefaultBaseURL is the production Connect origin.
const DefaultBaseURL = "https://connect.prusa3d.com"

// TokenSource supplies bearer tokens.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	Invalidate(access string)
}

// Client calls the Connect API.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	Tokens    TokenSource
	UserAgent string
}

// New builds a client; PRUSA_CONNECT_URL overrides the origin.
func New(tokens TokenSource, userAgent string) *Client {
	base := DefaultBaseURL
	if v := os.Getenv("PRUSA_CONNECT_URL"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &Client{
		BaseURL:   base,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		Tokens:    tokens,
		UserAgent: userAgent,
	}
}

// APIError is a non-2xx response.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := redact.Text(strings.TrimSpace(e.Body))
	if len(body) > 2000 {
		body = body[:2000] + "…"
	}
	msg := fmt.Sprintf("Prusa Connect: %s %s -> %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if body != "" {
		msg += ": " + body
	}
	return msg
}

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// Request describes one call. Body, when set, is re-created for the retry
// that follows an expired-token 401.
type Request struct {
	Method        string
	Path          string // "/app/..."
	Query         url.Values
	JSON          any                           // marshalled as the body
	Body          func() (io.ReadCloser, error) // raw body (uploads)
	ContentLength int64                         // for Body; avoids chunked uploads
	ContentType   string
	Header        http.Header
	Timeout       time.Duration // overrides the client timeout (uploads)
}

// Do performs req and returns the response with its body unread; the caller
// must close it. Non-2xx responses are returned as *APIError.
func (c *Client) Do(ctx context.Context, req Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		token, err := c.Tokens.AccessToken(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := c.send(ctx, req, token)
		if err != nil {
			return nil, err
		}
		// An expired or revoked access token: refresh once and retry. A 401
		// means the request was not processed, so retrying a mutation is safe.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.Tokens.Invalidate(token)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			return nil, &APIError{Method: req.Method, Path: req.Path, Status: resp.StatusCode, Body: string(b)}
		}
		return resp, nil
	}
}

func (c *Client) send(ctx context.Context, req Request, token string) (*http.Response, error) {
	u := c.BaseURL + req.Path
	if len(req.Query) > 0 {
		u += "?" + req.Query.Encode()
	}
	var body io.Reader
	contentType := req.ContentType
	switch {
	case req.Body != nil:
		rc, err := req.Body()
		if err != nil {
			return nil, err
		}
		body = rc
	case req.JSON != nil:
		b, err := json.Marshal(req.JSON)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		contentType = "application/json"
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, u, body)
	if err != nil {
		return nil, err
	}
	if req.ContentLength > 0 {
		hr.ContentLength = req.ContentLength
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	if contentType != "" {
		hr.Header.Set("Content-Type", contentType)
	}
	hr.Header.Set("Authorization", "Bearer "+token)
	hr.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		hr.Header.Set("User-Agent", c.UserAgent)
	}
	httpClient := c.HTTP
	if req.Timeout > 0 {
		clone := *c.HTTP
		clone.Timeout = req.Timeout
		httpClient = &clone
	}
	resp, err := httpClient.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("Prusa Connect: %s %s: %w", req.Method, req.Path, err)
	}
	return resp, nil
}

// JSON performs req and decodes a JSON response into out (which may be nil,
// or a *json.RawMessage to keep the payload verbatim).
func (c *Client) JSON(ctx context.Context, req Request, out any) error {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = json.RawMessage("null")
		}
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("Prusa Connect: %s %s returned unreadable JSON: %w", req.Method, req.Path, err)
	}
	return nil
}

// Get is shorthand for a GET returning JSON.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.JSON(ctx, Request{Method: http.MethodGet, Path: path, Query: query}, out)
}

// PathEscape escapes one path segment.
func PathEscape(s string) string { return url.PathEscape(s) }
