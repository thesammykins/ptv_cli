package ptvapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client is a signed HTTP client for the PTV Timetable API v3.
type Client struct {
	baseURL string
	apiKey  string
	devID   string
	http    *http.Client
}

// New constructs a Client with explicit transport timeouts so connectivity
// problems surface promptly rather than hanging on the default settings.
func New(baseURL, apiKey, devID string) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		devID:   devID,
		http:    &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// get performs a signed GET request against the given API path, decoding the
// JSON response into out. path must start with "/v3/".
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	signed := buildSignedURL(c.baseURL, c.apiKey, c.devID, path, query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signed, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var er ErrorResponse
		if json.Unmarshal(body, &er) == nil && er.Message != "" {
			return fmt.Errorf("PTV API error (%d): %s", resp.StatusCode, er.Message)
		}
		return fmt.Errorf("PTV API error (%d)", resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
