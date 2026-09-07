// Package knowledgeclient connects a running Agent to its scoped knowledge API.
package knowledgeclient

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
)

const maxResponseBytes = 8 << 20

type Client struct {
	BaseURL      string
	Token        string
	HTTP         *http.Client
	PollInterval time.Duration
}

func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("knowledge endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("knowledge access token is required")
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, PollInterval: time.Second}, nil
}

func (c *Client) Do(ctx context.Context, method, path string, input any, key string) (json.RawMessage, error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("knowledge response exceeds %d bytes; delivery is incomplete", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var p struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &p)
		return nil, fmt.Errorf("knowledge HTTP %d (%s): %s", resp.StatusCode, p.Code, p.Detail)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("knowledge service returned invalid JSON")
	}
	return raw, nil
}

// Ask waits for a durable knowledge job. Cancellation also requests that the
// control plane cancel its Run; abandoning the HTTP connection is insufficient.
func (c *Client) Ask(ctx context.Context, input any, key string) (json.RawMessage, error) {
	raw, err := c.Do(ctx, http.MethodPost, "/inquiries", input, key)
	if err != nil {
		return nil, err
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err = json.Unmarshal(raw, &job); err != nil || job.ID == "" {
		return nil, fmt.Errorf("knowledge inquiry response has no job identity")
	}
	jobPath := "/jobs/" + url.PathEscape(job.ID)
	defer func() {
		if ctx.Err() == nil {
			return
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Do(cancelCtx, http.MethodPost, jobPath+"/cancel", map[string]any{}, "cancel:"+job.ID)
	}()
	interval := c.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		switch job.Status {
		case "completed", "complete", "incomplete", "conflict", "failed", "cancelled":
			return raw, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			raw, err = c.Do(ctx, http.MethodGet, jobPath, nil, "")
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &job); err != nil {
				return nil, err
			}
		}
	}
}
