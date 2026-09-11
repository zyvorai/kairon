// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct{ HTTP *http.Client }

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{HTTP: httpClient}
}

func (c *Client) Prepare(ctx context.Context, baseURL string, session Session) (PrepareResponse, error) {
	var out PrepareResponse
	err := c.do(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/internal/v1/migrations/prepare", PrepareRequest{Session: session}, &out)
	return out, err
}

func (c *Client) Get(ctx context.Context, baseURL, id string) (PrepareResponse, error) {
	var out PrepareResponse
	err := c.do(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/internal/v1/migrations/"+id, nil, &out)
	return out, err
}

func (c *Client) Commit(ctx context.Context, baseURL, id string) error {
	return c.do(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/internal/v1/migrations/"+id+"/commit", map[string]any{}, nil)
}

func (c *Client) Abort(ctx context.Context, baseURL, id string) error {
	return c.do(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/internal/v1/migrations/"+id+"/abort", map[string]any{}, nil)
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("migration peer %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode migration peer response: %w", err)
		}
	}
	return nil
}
