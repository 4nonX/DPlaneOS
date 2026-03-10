// Package dockerclient provides a minimal Docker API client over the Unix socket.
//
// Why not the official Docker SDK?
//
//	The official docker/docker SDK pulls in golang.org/x/crypto and dozens of
//	other heavy dependencies. For a NAS daemon that just needs container
//	lifecycle management, a thin stdlib client is safer, faster to compile,
//	and has zero supply-chain surface beyond the Go standard library.
//
// API version: v1.41 (Docker Engine 20.10+). All NAS-relevant distros ship 20.10+.
package dockerclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	socketPath = "/var/run/docker.sock"
	apiVersion = "v1.41" // Docker Engine REST API version (Engine 20.10+)
)

// Client is a minimal Docker API client using the Unix socket.
type Client struct {
	http *http.Client
}

// New returns a Client connected to /var/run/docker.sock.
func New() *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
		},
	}
}

// ─────────────────────────────────────────────
//  Request helpers
// ─────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func (c *Client) post(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

func decodeJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func expectOK(resp *http.Response) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// ─────────────────────────────────────────────
//  Container types
// ─────────────────────────────────────────────

// Container is the condensed container summary from /containers/json.
type Container struct {
	ID         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	ImageID    string            `json:"ImageID"`
	Command    string            `json:"Command"`
	Created    int64             `json:"Created"`
	State      string            `json:"State"`  // "running", "exited", etc.
	Status     string            `json:"Status"` // human-readable e.g. "Up 2 hours"
	Ports      []ContainerPort   `json:"Ports"`
	Labels     map[string]string `json:"Labels"`
	Mounts     []Mount           `json:"Mounts"`
	SizeRw     int64             `json:"SizeRw,omitempty"`
	SizeRootFs int64             `json:"SizeRootFs,omitempty"`
}

// ShortName returns the container's primary name without the leading slash.
func (c Container) ShortName() string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) >= 12 {
		return c.ID[:12]
	}
	return c.ID
}

// StackName returns the docker-compose project name from labels, or "ungrouped".
func (c Container) StackName() string {
	if v, ok := c.Labels["com.docker.compose.project"]; ok && v != "" {
		return v
	}
	if v, ok := c.Labels["stack"]; ok && v != "" {
		return v
	}
	return "ungrouped"
}

type ContainerPort struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

// ContainerDetail is the full inspect response from /containers/{id}/json.
type ContainerDetail struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	State   struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Dead       bool   `json:"Dead"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"` // "healthy", "unhealthy", "starting"
		} `json:"Health,omitempty"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
		Env    []string          `json:"Env"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Binds        []string               `json:"Binds"`
		PortBindings map[string]interface{} `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []Mount `json:"Mounts"`
}

// ─────────────────────────────────────────────
//  Container operations
// ─────────────────────────────────────────────

// ListAll returns all containers (running + stopped).
func (c *Client) ListAll(ctx context.Context) ([]Container, error) {
	resp, err := c.get(ctx, "/containers/json", url.Values{"all": {"1"}})
	if err != nil {
		return nil, fmt.Errorf("docker list: %w", err)
	}
	var containers []Container
	if err := decodeJSON(resp, &containers); err != nil {
		return nil, fmt.Errorf("docker list decode: %w", err)
	}
	return containers, nil
}

// Inspect returns full details for a single container.
func (c *Client) Inspect(ctx context.Context, id string) (*ContainerDetail, error) {
	if id == "" {
		return nil, fmt.Errorf("container id is required")
	}
	resp, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var detail ContainerDetail
	if err := decodeJSON(resp, &detail); err != nil {
		return nil, fmt.Errorf("docker inspect decode: %w", err)
	}
	return &detail, nil
}

// Start starts a stopped container.
func (c *Client) Start(ctx context.Context, id string) error {
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/start", nil)
	if err != nil {
		return fmt.Errorf("docker start: %w", err)
	}
	// 204 = started, 304 = already running — both OK
	if resp.StatusCode == 304 {
		resp.Body.Close()
		return nil
	}
	return expectOK(resp)
}

// Stop stops a running container. timeoutSec = seconds to wait before SIGKILL (0 = immediate).
func (c *Client) Stop(ctx context.Context, id string, timeoutSec int) error {
	q := url.Values{}
	if timeoutSec > 0 {
		q.Set("t", fmt.Sprintf("%d", timeoutSec))
	}
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/stop", q)
	if err != nil {
		return fmt.Errorf("docker stop: %w", err)
	}
	if resp.StatusCode == 304 {
		resp.Body.Close()
		return nil // already stopped
	}
	return expectOK(resp)
}

// Restart restarts a container.
func (c *Client) Restart(ctx context.Context, id string, timeoutSec int) error {
	q := url.Values{}
	if timeoutSec > 0 {
		q.Set("t", fmt.Sprintf("%d", timeoutSec))
	}
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/restart", q)
	if err != nil {
		return fmt.Errorf("docker restart: %w", err)
	}
	return expectOK(resp)
}

// Pause pauses a running container.
func (c *Client) Pause(ctx context.Context, id string) error {
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/pause", nil)
	if err != nil {
		return fmt.Errorf("docker pause: %w", err)
	}
	return expectOK(resp)
}

// Unpause unpauses a paused container.
func (c *Client) Unpause(ctx context.Context, id string) error {
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/unpause", nil)
	if err != nil {
		return fmt.Errorf("docker unpause: %w", err)
	}
	return expectOK(resp)
}

// ─────────────────────────────────────────────
//  Logs
// ─────────────────────────────────────────────

// LogOptions controls log retrieval.
type LogOptions struct {
	Stdout     bool
	Stderr     bool
	Tail       string // "100" or "all"
	Since      string // Unix timestamp or duration e.g. "1h"
	Timestamps bool
}

// Logs fetches container logs, stripping Docker's 8-byte stream multiplexing header.
func (c *Client) Logs(ctx context.Context, id string, opts LogOptions) (string, error) {
	if id == "" {
		return "", fmt.Errorf("container id is required")
	}
	q := url.Values{}
	if opts.Stdout {
		q.Set("stdout", "1")
	}
	if opts.Stderr {
		q.Set("stderr", "1")
	}
	tail := opts.Tail
	if tail == "" {
		tail = "100"
	}
	q.Set("tail", tail)
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}

	resp, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/logs", q)
	if err != nil {
		return "", fmt.Errorf("docker logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("docker logs %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var lines []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip Docker's 8-byte stream multiplexing header:
		// byte 0: stream type (1=stdout, 2=stderr), bytes 1-3: padding, bytes 4-7: uint32 size
		if len(line) > 8 {
			b := line[0]
			if b == 1 || b == 2 {
				line = line[8:]
			}
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("docker logs read: %w", err)
	}
	return strings.Join(lines, "\n"), nil
}

// ─────────────────────────────────────────────
//  Image operations
// ─────────────────────────────────────────────

// PullImage pulls an image. Blocks until the pull is complete.
func (c *Client) PullImage(ctx context.Context, image string) error {
	q := url.Values{"fromImage": {image}}
	longClient := &http.Client{
		Transport: c.http.Transport,
		Timeout:   10 * time.Minute,
	}
	u := "http://docker/" + apiVersion + "/images/create?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return fmt.Errorf("docker pull: %w", err)
	}
	resp, err := longClient.Do(req)
	if err != nil {
		return fmt.Errorf("docker pull: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker pull %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	io.Copy(io.Discard, resp.Body) // drain progress stream
	return nil
}

// ─────────────────────────────────────────────
//  System
// ─────────────────────────────────────────────

// Info returns Docker system information.
func (c *Client) Info(ctx context.Context) (map[string]interface{}, error) {
	resp, err := c.get(ctx, "/info", nil)
	if err != nil {
		return nil, fmt.Errorf("docker info: %w", err)
	}
	var info map[string]interface{}
	if err := decodeJSON(resp, &info); err != nil {
		return nil, fmt.Errorf("docker info decode: %w", err)
	}
	return info, nil
}

// IsAvailable returns true if the Docker socket is reachable.
func (c *Client) IsAvailable(ctx context.Context) bool {
	resp, err := c.get(ctx, "/ping", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// WaitForHealthy polls until the container is running and healthy.
// Respects Docker HEALTHCHECK: if defined, waits for "healthy" not just "running".
func (c *Client) WaitForHealthy(ctx context.Context, id string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, err := c.Inspect(ctx, id)
		if err != nil {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return false, ctx.Err()
			}
			continue
		}
		if !detail.State.Running {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return false, ctx.Err()
			}
			continue
		}
		if detail.State.Health == nil {
			return true, nil // no HEALTHCHECK — running is sufficient
		}
		switch detail.State.Health.Status {
		case "healthy":
			return true, nil
		case "unhealthy":
			return false, fmt.Errorf("container health check failed (status: unhealthy)")
		default:
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return false, ctx.Err()
			} // "starting" — keep polling
		}
	}
	return false, fmt.Errorf("timed out after %v", timeout)
}

// Remove removes a container.
func (c *Client) Remove(ctx context.Context, id string, force bool, removeVolumes bool) error {
	if id == "" {
		return fmt.Errorf("container id is required")
	}
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	if removeVolumes {
		q.Set("v", "1")
	}
	u := "http://docker/" + apiVersion + "/containers/" + url.PathEscape(id)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("docker remove: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker remove: %w", err)
	}
	if resp.StatusCode == 404 {
		resp.Body.Close()
		return nil // already gone
	}
	return expectOK(resp)
}

// PruneResult holds space reclaimed by a prune operation.
type PruneResult struct {
	SpaceReclaimed int64 `json:"space_reclaimed"`
	ItemsRemoved   int   `json:"items_removed"`
}

// PruneAll removes stopped containers, dangling images, and unused volumes.
// Returns aggregate counts and space reclaimed.
func (c *Client) PruneAll(ctx context.Context) (containers, images, volumes int, spaceBytes int64, err error) {
	// 1. Prune stopped containers
	resp, e := c.post(ctx, "/containers/prune", nil)
	if e == nil {
		var r struct {
			ContainersDeleted []string `json:"ContainersDeleted"`
			SpaceReclaimed    int64    `json:"SpaceReclaimed"`
		}
		if decodeJSON(resp, &r) == nil {
			containers = len(r.ContainersDeleted)
			spaceBytes += r.SpaceReclaimed
		}
	}

	// 2. Prune dangling images
	q := url.Values{}
	q.Set("filters", `{"dangling":["true"]}`)
	resp, e = c.post(ctx, "/images/prune", q)
	if e == nil {
		var r struct {
			ImagesDeleted  []interface{} `json:"ImagesDeleted"`
			SpaceReclaimed int64         `json:"SpaceReclaimed"`
		}
		if decodeJSON(resp, &r) == nil {
			images = len(r.ImagesDeleted)
			spaceBytes += r.SpaceReclaimed
		}
	}

	// 3. Prune unused volumes
	resp, e = c.post(ctx, "/volumes/prune", nil)
	if e == nil {
		var r struct {
			VolumesDeleted []string `json:"VolumesDeleted"`
			SpaceReclaimed int64    `json:"SpaceReclaimed"`
		}
		if decodeJSON(resp, &r) == nil {
			volumes = len(r.VolumesDeleted)
			spaceBytes += r.SpaceReclaimed
		}
	}

	return containers, images, volumes, spaceBytes, nil
}
