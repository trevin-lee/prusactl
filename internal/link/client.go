// Package link talks to a printer directly over the local network through
// PrusaLink, the HTTP API built into Prusa's firmware
// (https://github.com/prusa3d/Prusa-Link-Web/blob/master/spec/openapi.yaml).
package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/trevin-lee/prusactl/internal/compat"
	"github.com/trevin-lee/prusactl/internal/redact"
)

// Client is a PrusaLink client.
type Client struct {
	Config Config
	secret string
	HTTP   *http.Client

	mu sync.Mutex
	ch *challenge
	nc uint32
}

// New builds a client for cfg with its secret.
func New(cfg Config, secret string) *Client {
	return &Client{Config: cfg, secret: secret, HTTP: &http.Client{
		Timeout: 30 * time.Second,
		// PrusaLink never redirects; following one could leak credentials.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Open loads the saved configuration and secret.
func Open() (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	secret, err := cfg.Secret()
	if err != nil {
		return nil, err
	}
	return New(cfg, secret), nil
}

// Same reports whether o talks to the same printer with the same credentials.
func (c *Client) Same(o *Client) bool {
	return c != nil && o != nil && c.Config == o.Config && c.secret == o.secret
}

// APIError is a non-2xx response.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("printer: %s %s -> %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if b := redact.Text(strings.TrimSpace(e.Body)); b != "" {
		if len(b) > 1000 {
			b = b[:1000] + "…"
		}
		msg += ": " + b
	}
	if e.Status == http.StatusUnauthorized {
		msg += " (wrong password? run `prusactl setup` again)"
	}
	return msg
}

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// Request describes one call.
type Request struct {
	Method        string
	Path          string // "/api/v1/..."
	Query         url.Values
	Body          func() (io.ReadCloser, error) // re-created for the Digest retry
	ContentLength int64
	ContentType   string
	Header        http.Header
	Timeout       time.Duration
}

// Do sends req, answering Digest challenges. Non-2xx statuses are returned as
// *APIError; the caller closes the body of successful responses.
func (c *Client) Do(ctx context.Context, req Request) (*http.Response, error) {
	// Uploads must not be sent twice just to learn the nonce: fetch a
	// challenge first with a cheap request.
	if c.Config.Auth != AuthAPIKey && req.Body != nil && c.currentChallenge() == nil {
		if resp, err := c.send(ctx, Request{Method: http.MethodGet, Path: "/api/version"}); err == nil {
			resp.Body.Close()
		}
	}
	resp, err := c.send(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.Config.Auth != AuthAPIKey {
		// First contact, or the nonce went stale: answer the new challenge once.
		if ch, ok := parseChallenge(resp.Header.Get("WWW-Authenticate")); ok {
			resp.Body.Close()
			c.setChallenge(ch)
			if resp, err = c.send(ctx, req); err != nil {
				return nil, err
			}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		apiErr := &APIError{Method: req.Method, Path: req.Path, Status: resp.StatusCode, Body: string(b)}
		if missingEndpoint(req.Path, resp.StatusCode) {
			return nil, &compat.Error{Service: compat.Printer, Detail: fmt.Sprintf("%s %s answered %d", req.Method, req.Path, resp.StatusCode), Err: apiErr}
		}
		return nil, apiErr
	}
	return resp, nil
}

// fixedEndpoints always exist in PrusaLink API v1, so a 404 or 405 from one of
// them means the firmware changed the API. Anything under /api/v1/files can
// legitimately be missing (a file that isn't there), so it doesn't count.
var fixedEndpoints = map[string]bool{
	"/api/version":     true,
	"/api/v1/info":     true,
	"/api/v1/status":   true,
	"/api/v1/storage":  true,
	"/api/v1/job":      true,
	"/api/v1/transfer": true,
}

func missingEndpoint(path string, status int) bool {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return false
	}
	return fixedEndpoints[path]
}

func (c *Client) currentChallenge() *challenge {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ch
}

func (c *Client) setChallenge(ch *challenge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ch, c.nc = ch, 0
}

func (c *Client) send(ctx context.Context, req Request) (*http.Response, error) {
	uri := req.Path
	if len(req.Query) > 0 {
		uri += "?" + req.Query.Encode()
	}
	var body io.Reader
	if req.Body != nil {
		rc, err := req.Body()
		if err != nil {
			return nil, err
		}
		body = rc
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, c.Config.Host+uri, body)
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
	if req.ContentType != "" {
		hr.Header.Set("Content-Type", req.ContentType)
	}
	if hr.Header.Get("Accept") == "" {
		hr.Header.Set("Accept", "application/json")
	}
	switch {
	case c.Config.Auth == AuthAPIKey:
		hr.Header.Set("X-Api-Key", c.secret)
	default:
		c.mu.Lock()
		if c.ch != nil {
			c.nc++
			hr.Header.Set("Authorization", c.ch.authorization(c.Config.User, c.secret, req.Method, uri, c.nc))
		}
		c.mu.Unlock()
	}
	httpClient := c.HTTP
	if req.Timeout > 0 {
		clone := *c.HTTP
		clone.Timeout = req.Timeout
		httpClient = &clone
	}
	resp, err := httpClient.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("printer at %s: %w", c.Config.Host, err)
	}
	return resp, nil
}

// JSON performs req and decodes the response into out (nil to discard; a
// *json.RawMessage keeps it verbatim). 204 No Content leaves out untouched
// and reports found=false.
func (c *Client) JSON(ctx context.Context, req Request, out any) (found bool, err error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return false, err
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return true, nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false, &compat.Error{Service: compat.Printer, Detail: fmt.Sprintf("%s %s returned data in a different format (%v)", req.Method, req.Path, err), Err: err}
	}
	return true, nil
}

// Get is shorthand for a GET returning JSON.
func (c *Client) Get(ctx context.Context, path string, out any) (bool, error) {
	return c.JSON(ctx, Request{Method: http.MethodGet, Path: path}, out)
}

// FilePath builds /api/v1/files/{storage}/{path} from a printer path such as
// "/usb/parts/bracket.bgcode".
func FilePath(printerPath string) (string, error) {
	p := strings.TrimSpace(printerPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return "", fmt.Errorf("invalid path %q", printerPath)
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if parts[0] == "" {
		return "", fmt.Errorf("path %q has no storage (e.g. /usb/file.bgcode)", printerPath)
	}
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/api/v1/files/" + strings.Join(parts, "/"), nil
}
