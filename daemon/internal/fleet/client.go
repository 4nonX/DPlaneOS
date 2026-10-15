package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The manager reaches a remote through the remote's ordinary API with an
// API token (admin scope) created on the remote. HTTPS certificates of nodes
// are usually self-signed: the certificate seen when the remote is added is
// pinned, and any other certificate is refused afterwards.

// NodeStatus is a remote's inventory report (GET /api/fleet/node-status).
type NodeStatus struct {
	NodeID      string       `json:"node_id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Mode        string       `json:"mode"` // standalone, connected, isolated, independent
	Cluster     string       `json:"cluster,omitempty"`
	Quorate     bool         `json:"quorate"`
	Coordinator bool         `json:"coordinator"`
	Maintenance bool         `json:"maintenance"`
	Pools       []PoolHealth `json:"pools"`
	Groups      []string     `json:"groups_owned"`
	Conflicts   int          `json:"conflicts"`
	Proposals   int          `json:"proposals"`
	Holds       int          `json:"holds"`
	Fleet       string       `json:"fleet,omitempty"` // the fleet that last applied configuration here
	ReportedAt  time.Time    `json:"reported_at"`
}

// PoolHealth is one pool of a remote.
type PoolHealth struct {
	Name   string `json:"name"`
	Health string `json:"health"`
}

// Healthy reports whether nothing on the remote needs attention.
func (s NodeStatus) Healthy() bool {
	for _, p := range s.Pools {
		if p.Health != "ONLINE" {
			return false
		}
	}
	return s.Mode != "isolated" && s.Holds == 0 && s.Conflicts == 0 && (s.Cluster == "" || s.Quorate)
}

// Headers by which a manager identifies itself to a remote.
const (
	HeaderFleet   = "X-DPlane-Fleet"
	HeaderManager = "X-DPlane-Fleet-Manager"
)

// ErrPinMismatch: the remote presented another certificate than the pinned one.
var ErrPinMismatch = errors.New("the remote's TLS certificate changed")

// Client calls one remote.
type Client struct {
	URL         string
	Token       string
	Fingerprint string // pinned; "" only while adding (trust on first use)
	Timeout     time.Duration
	// Fleet and Manager identify the caller, so the remote can tell when
	// its manager has gone quiet.
	Fleet, Manager string

	seen string
}

// ValidURL accepts http(s) URLs without credentials, path or query.
func ValidURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("invalid remote address %q (https://host[:port])", raw)
	}
	return nil
}

// Seen is the certificate fingerprint of the last connection (https).
func (c *Client) Seen() string { return c.seen }

func (c *Client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Fleet != "" {
		req.Header.Set(HeaderFleet, c.Fleet)
		req.Header.Set(HeaderManager, c.Manager)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin check
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate presented")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			c.seen = hex.EncodeToString(sum[:])
			if c.Fingerprint != "" && c.seen != c.Fingerprint {
				return ErrPinMismatch
			}
			return nil
		},
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the remote refused the token (HTTP %d): it needs an API token with admin scope", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from the remote: %w", err)
		}
	}
	return nil
}

// Status reads the remote's inventory report.
func (c *Client) Status() (*NodeStatus, error) {
	var resp struct {
		Success bool       `json:"success"`
		Status  NodeStatus `json:"status"`
		Error   string     `json:"error"`
	}
	if err := c.do("GET", "/api/fleet/node-status", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}
	return &resp.Status, nil
}

// Resource is one fleet resource on the wire.
type Resource struct {
	Kind    string         `json:"kind"`
	Key     string         `json:"key"`
	Payload map[string]any `json:"payload"` // nil = delete
	Version int64          `json:"version,omitempty"`
}

// ApplyResult is the remote's answer per resource.
type ApplyResult struct {
	Kind     string `json:"kind"`
	Key      string `json:"key"`
	Result   string `json:"result"` // applied, unchanged, failed
	Detail   string `json:"detail,omitempty"`
	Proposal bool   `json:"proposal,omitempty"`
}

// Apply sends fleet resources to the remote.
func (c *Client) Apply(fleet, manager, rollout string, rs []Resource) ([]ApplyResult, error) {
	var resp struct {
		Success bool          `json:"success"`
		Error   string        `json:"error"`
		Results []ApplyResult `json:"results"`
	}
	c.Timeout = 5 * time.Minute
	err := c.do("POST", "/api/fleet/apply", map[string]any{"fleet": fleet, "manager": manager, "rollout": rollout, "resources": rs}, &resp)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Results, errors.New(resp.Error)
	}
	return resp.Results, nil
}
