// Package client is the thin HTTP client every CLI subcommand uses to talk
// to the PersonalS3 API.
//
// Responsibilities:
//   - Build URLs from the configured server base
//   - Attach the Authorization header (JWT bearer)
//   - Decode error responses uniformly
//   - Stream bodies for uploads/downloads (no in-memory buffering of large files)
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/personals3/cli/internal/config"
)

type Client struct {
	cfg  *config.Config
	http *http.Client
}

func New(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			// No global timeout — multipart uploads of large files can take
			// many minutes. Individual operations can pass their own context.
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// APIErr is what the API returns in its JSON error envelope.
type APIErr struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIErr) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Login POSTs /auth/login. Returns:
//
//   - "<jwt>"      on a plain-password account
//   - "2fa:<challenge>" if the account has TOTP 2FA enabled — caller should
//                  prompt for the code and call Verify2FA
//
// Does NOT persist anything; the caller saves to config.
func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		c.cfg.Server+"/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", parseErr(resp)
	}
	var r struct {
		Token      string `json:"token"`
		Require2FA bool   `json:"require2fa"`
		Challenge  string `json:"challenge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Require2FA && r.Challenge != "" {
		return "2fa:" + r.Challenge, nil
	}
	if r.Token == "" {
		return "", fmt.Errorf("no token in response")
	}
	return r.Token, nil
}

// Verify2FA exchanges a (challenge, code) pair for the final JWT.
func (c *Client) Verify2FA(ctx context.Context, challenge, code string) (string, error) {
	body, _ := json.Marshal(map[string]string{"challenge": challenge, "code": code})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		c.cfg.Server+"/api/auth/2fa/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", parseErr(resp)
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Token, nil
}

// Do is the generic request helper. Sets Authorization, decodes JSON into
// `out` (any pointer), surfaces API errors. Pass `body=nil` for GET/DELETE.
func (c *Client) Do(ctx context.Context, method, urlPath string, body any, out any) error {
	return c.DoRaw(ctx, method, urlPath, body, "application/json", out, nil)
}

// DoRaw is the streaming variant — body can be an io.Reader (for uploads),
// out can be nil (for "don't decode"), or a *io.Reader-style sink callback
// can be provided via onResp.
//
// onResp(resp) is called BEFORE the body is read so the caller can swap in
// streaming behavior (e.g. download to disk). If onResp is nil and out is
// non-nil, the body is JSON-decoded into out.
func (c *Client) DoRaw(ctx context.Context, method, urlPath string, body any,
	contentType string, out any,
	onResp func(*http.Response) error,
) error {
	full := c.cfg.Server + urlPath
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case io.Reader:
		reader = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	if reader != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	if onResp != nil {
		return onResp(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Download streams a GET response body to a writer (e.g. an open file).
func (c *Client) Download(ctx context.Context, urlPath string, w io.Writer) (int64, error) {
	var total int64
	err := c.DoRaw(ctx, "GET", urlPath, nil, "", nil, func(resp *http.Response) error {
		n, err := io.Copy(w, resp.Body)
		total = n
		return err
	})
	return total, err
}

// Upload streams a reader body into a PUT request. contentType optional.
func (c *Client) Upload(ctx context.Context, urlPath string, r io.Reader,
	contentLength int64, contentType string) error {
	full := c.cfg.Server + urlPath
	req, err := http.NewRequestWithContext(ctx, "PUT", full, r)
	if err != nil {
		return err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	return nil
}

func parseErr(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	e := &APIErr{Status: resp.StatusCode}
	if len(b) > 0 {
		_ = json.Unmarshal(b, e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
	}
	return e
}

// EncodeKey URL-escapes each path segment of a key. Required because keys
// can contain spaces / unicode / # and the chi catch-all sees them raw.
func EncodeKey(k string) string {
	parts := strings.Split(k, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
