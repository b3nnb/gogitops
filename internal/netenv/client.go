// Package netenv provides a client for the netenv key rotation and settings server.
package netenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client connects to a netenv server and reports key rotation state.
type Client struct {
	BaseURL    string
	Token      string
	Hostname   string
	HTTPClient *http.Client
}

// NewClient creates a netenv client.
func NewClient(baseURL, token, hostname string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Token:    token,
		Hostname: hostname,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// KeyState represents the encryption key state from netenv.
type KeyState struct {
	ActiveVersion int       `json:"active_version"`
	Keys          []KeyInfo `json:"keys"`
}

// KeyInfo is a single key version.
type KeyInfo struct {
	Version      int    `json:"version"`
	Status       string `json:"status"`
	SecretsUsing int    `json:"secrets_using"`
}

// RotationState represents the rotation scheduler state.
type RotationState struct {
	Phase         string     `json:"phase"`
	RotationDays  int        `json:"rotation_days"`
	NextRotation  *time.Time `json:"next_rotation"`
	Overdue       bool       `json:"overdue"`
	ActiveVersion int        `json:"active_version"`
	ProgressPct   int        `json:"progress_pct"`
}

// GetKeys fetches the current key state.
func (c *Client) GetKeys() (*KeyState, error) {
	var ks KeyState
	if err := c.get("/v1/keys", &ks); err != nil {
		return nil, err
	}
	return &ks, nil
}

// GetRotationState fetches the rotation scheduler state.
func (c *Client) GetRotationState() (*RotationState, error) {
	var rs RotationState
	if err := c.get("/v1/rotation", &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

// CheckIn reports the node's key version to netenv.
func (c *Client) CheckIn(keyVersion int) error {
	body := map[string]interface{}{
		"hostname":    c.Hostname,
		"key_version": keyVersion,
	}
	return c.post("/v1/nodes/"+c.Hostname+"/checkin", body, nil)
}

// Resolve fetches secrets from the given namespaces (comma-separated).
func (c *Client) Resolve(namespaces string) (map[string]string, error) {
	resp, err := c.doRequest("GET", "/v1/resolve/"+namespaces, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resolve failed: %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Response is KEY=value\n format
	result := map[string]string{}
	lines := string(data)
	for _, line := range splitLines(lines) {
		if idx := indexOfEq(line); idx > 0 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	return result, nil
}

func (c *Client) get(path string, out interface{}) error {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("netenv %s: %s", path, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(path string, body interface{}, out interface{}) error {
	resp, err := c.doRequest("POST", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("netenv %s: %s", path, resp.Status)
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.HTTPClient.Do(req)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// ResolveReference resolves a nenv:ns/KEY reference by fetching from netenv.
// If the value doesn't start with "nenv:", it's returned as-is.
func (c *Client) ResolveReference(ref string) (string, error) {
	if len(ref) < 5 || ref[:5] != "nenv:" {
		return ref, nil // not a reference, return as-is
	}
	// Parse nenv:namespace/key
	parts := ref[5:]
	slashIdx := indexOfSlash(parts)
	if slashIdx < 0 {
		return "", fmt.Errorf("invalid nenv reference %q: expected nenv:namespace/key", ref)
	}
	ns := parts[:slashIdx]
	key := parts[slashIdx+1:]

	var result struct {
		Value string `json:"value"`
	}
	if err := c.get("/v1/namespaces/"+ns+"/secrets/"+key, &result); err != nil {
		return "", fmt.Errorf("resolve nenv:%s/%s: %w", ns, key, err)
	}
	return result.Value, nil
}

func indexOfSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func indexOfEq(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return i
		}
	}
	return -1
}
