package security

import "testing"

func TestDockerCompose(t *testing.T) {
	ok := [][]string{
		{"compose", "--project-directory", "/var/lib/dplaneos/stacks/plex", "-f", "/var/lib/dplaneos/stacks/plex/docker-compose.yml", "up", "-d", "--remove-orphans"},
		{"compose", "--project-directory", "/var/lib/dplaneos/stacks/plex", "-f", "/var/lib/dplaneos/stacks/plex/docker-compose.yml", "ps", "--format", "json"},
		{"compose", "--project-directory", "/opt/stacks/app", "-f", "/opt/stacks/app/docker-compose.yml", "down", "-v"},
		{"compose", "-f", "/var/lib/dplaneos/git-stacks/repo/docker-compose.yaml", "up", "-d", "--remove-orphans"},
		{"compose", "--project-directory", "/mnt/tank/apps/x", "-f", "/mnt/tank/apps/x/docker-compose.yml", "restart", "web"},
	}
	for _, a := range ok {
		if err := ValidateCommand("docker_compose", a); err != nil {
			t.Errorf("%v rejected: %v", a, err)
		}
	}
	bad := [][]string{
		{"run", "--privileged", "alpine"},
		{"compose", "--project-directory", "/etc", "-f", "/etc/docker-compose.yml", "up"},
		{"compose", "-f", "/var/lib/dplaneos/stacks/../../../etc/x.yml", "up"},
		{"compose", "-f", "/var/lib/dplaneos/stacks/x/docker-compose.yml", "exec", "web", "sh"},
		{"compose", "-f", "/var/lib/dplaneos/stacks/x/docker-compose.yml", "up", "--privileged"},
		{"compose", "-H", "tcp://evil", "-f", "/var/lib/dplaneos/stacks/x/docker-compose.yml", "up"},
		{"compose", "-f", "/var/lib/dplaneos/stacks/x/notcompose.sh", "up"},
	}
	for _, a := range bad {
		if err := ValidateCommand("docker_compose", a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
}

func TestDockerCLI(t *testing.T) {
	ok := [][]string{
		{"ps", "--format", "{{.Names}}"},
		{"ps", "--filter", "label=com.docker.compose.project=plex", "--format", `{{.Label "dplaneos.icon"}}`, "--no-trunc"},
		{"stats", "--no-stream", "--format", `{"name":"{{.Name}}"}`},
		{"inspect", "plex", "jellyfin-1"},
	}
	for _, a := range ok {
		if err := ValidateCommand("docker_cli", a); err != nil {
			t.Errorf("%v rejected: %v", a, err)
		}
	}
	bad := [][]string{
		{"run", "alpine"},
		{"exec", "plex", "sh"},
		{"inspect", "--privileged"},
		{"inspect", "a;rm -rf /"},
		{"ps", "--format"},
	}
	for _, a := range bad {
		if err := ValidateCommand("docker_cli", a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
}
