// Package arr polls a Sonarr/Radarr instance's history for recently-changed
// file paths and rewrites them to Silo-native library paths.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxResponseBody caps the arr history response read to 1 MiB.
const maxResponseBody = 1 << 20

// defaultTimeout bounds a single arr HTTP request.
const defaultTimeout = 30 * time.Second

// client is a minimal arr HTTP client. It authenticates with the X-Api-Key
// header and caps the response body. Credentials are passed per call (the
// plugin stores none).
type client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newClient(baseURL, apiKey string, httpClient *http.Client) *client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

// getJSON issues a GET against the arr API and decodes the JSON response into
// dest. The response body is limited to maxResponseBody bytes.
func (c *client) getJSON(ctx context.Context, path string, dest any) error {
	if c.baseURL == "" {
		return fmt.Errorf("arr: base url is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("arr: api key is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("arr: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("arr: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		return fmt.Errorf("arr: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest); err != nil {
		return fmt.Errorf("arr: decode response: %w", err)
	}
	return nil
}
