package gitops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/config"
)

// docker_state.go — Desired state types, YAML parser, and validation for the
// Docker GitOps subsystem.

const defaultStacksDir = config.StacksDir

// LiveStack represents the observed state of a Docker Compose stack.
type LiveStack struct {
	Name     string
	Path     string
	YAML     string
	Status   string // "running", "partial", "stopped"
	Services []string // names of services in this stack
}

// readLiveStacks scans /var/lib/dplaneos/stacks for directories containing
// docker-compose.yml and queries their current status.
func readLiveStacks() ([]LiveStack, error) {
	if err := os.MkdirAll(defaultStacksDir, 0750); err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(defaultStacksDir)
	if err != nil {
		return nil, err
	}

	var stacks []LiveStack
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(defaultStacksDir, name)
		composePath := filepath.Join(dir, "docker-compose.yml")

		if _, err := os.Stat(composePath); err != nil {
			continue
		}

		ls := LiveStack{
			Name: name,
			Path: dir,
		}

		// Read YAML (optional)
		if data, err := os.ReadFile(composePath); err == nil {
			ls.YAML = string(data)
		}

		// Get status via docker compose ps
		output, err := cmdutil.RunFast("/usr/bin/docker",
			"compose", "--project-directory", dir, "-f", composePath, "ps", "--format", "json")
		if err == nil {
			ls.Services, ls.Status = parseStackStatus(output)
		} else {
			ls.Status = "stopped"
		}

		stacks = append(stacks, ls)
	}

	return stacks, nil
}

func parseStackStatus(output []byte) ([]string, string) {
	var services []string
	running := 0
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var s struct {
			Service string `json:"Service"`
			State   string `json:"State"`
		}
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			services = append(services, s.Service)
			if s.State == "running" {
				running++
			}
		}
	}

	status := "stopped"
	if len(services) > 0 {
		if running == len(services) {
			status = "running"
		} else if running > 0 {
			status = "partial"
		}
	}

	return services, status
}