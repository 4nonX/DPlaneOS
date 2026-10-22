package security

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Docker commands. All callers go through the whitelist keys docker_compose
// and docker_cli (before, they called "docker" directly and bypassed it).

// ComposeRoots are the directories compose projects may live in (the same as
// the stack and compose handlers accept).
var ComposeRoots = []string{"/opt/", "/srv/", "/home/", "/var/lib/dplaneos/", "/mnt/", "/data/", "/tank/", "/pool/"}

var (
	composeSubcommands = map[string]bool{"up": true, "down": true, "ps": true, "start": true, "stop": true, "restart": true, "pull": true,
		"config": true, "images": true}
	composeFlags = map[string]bool{"-d": true, "--remove-orphans": true, "-v": true, "--format": true, "json": true,
		"--all": true, "-q": true, "--quiet": true}
	serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// composeProjectRe: compose's own rule for project names (-p).
	composeProjectRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	dockerCLISubs     = map[string]bool{"ps": true, "stats": true, "inspect": true}
	dockerCLIFlags    = map[string]bool{"--no-stream": true, "--no-trunc": true, "-a": true, "--all": true}
	dockerCLIValFlags = map[string]bool{"--format": true, "--filter": true}
)

func validComposePath(p string, file bool) error {
	if p == "" || strings.ContainsAny(p, "\x00\n") || !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "..") {
		return fmt.Errorf("invalid compose path %q", p)
	}
	if file && !strings.HasSuffix(p, ".yml") && !strings.HasSuffix(p, ".yaml") {
		return fmt.Errorf("compose file must be .yml or .yaml: %q", p)
	}
	for _, root := range ComposeRoots {
		if strings.HasPrefix(p+"/", root) {
			return nil
		}
	}
	return fmt.Errorf("compose path %q is outside the allowed directories", p)
}

// validEnvFilePath: an --env-file under the compose roots.
func validEnvFilePath(p string) error {
	if p == "" || strings.ContainsAny(p, "\x00\n") || !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "..") {
		return fmt.Errorf("invalid env file path %q", p)
	}
	for _, root := range ComposeRoots {
		if strings.HasPrefix(p, root) {
			return nil
		}
	}
	return fmt.Errorf("env file %q is outside the allowed directories", p)
}

// validateDockerCompose: compose [-p NAME] [--project-directory DIR] [-f FILE]...
// [--env-file FILE] SUB [flags|services]
func validateDockerCompose(args []string) error {
	if len(args) < 2 || args[0] != "compose" {
		return fmt.Errorf("docker_compose: must start with compose")
	}
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if i+1 >= len(args) {
			return fmt.Errorf("docker_compose: %s needs a value", args[i])
		}
		switch args[i] {
		case "--project-directory":
			if err := validComposePath(args[i+1], false); err != nil {
				return err
			}
		case "-f":
			if err := validComposePath(args[i+1], true); err != nil {
				return err
			}
		case "-p":
			if !composeProjectRe.MatchString(args[i+1]) {
				return fmt.Errorf("docker_compose: invalid project name %q", args[i+1])
			}
		case "--env-file":
			if err := validEnvFilePath(args[i+1]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("docker_compose: option %q not allowed", args[i])
		}
		i += 2
	}
	if i >= len(args) || !composeSubcommands[args[i]] {
		return fmt.Errorf("docker_compose: subcommand not allowed")
	}
	for _, a := range args[i+1:] {
		if !composeFlags[a] && !serviceNameRe.MatchString(a) {
			return fmt.Errorf("docker_compose: argument %q not allowed", a)
		}
	}
	return nil
}

// validateDockerCLI: ps | stats | inspect with display options and names.
func validateDockerCLI(args []string) error {
	if len(args) == 0 || !dockerCLISubs[args[0]] {
		return fmt.Errorf("docker_cli: subcommand not allowed")
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case dockerCLIFlags[a]:
		case dockerCLIValFlags[a]:
			if i+1 >= len(args) || strings.ContainsAny(args[i+1], "\x00\n") {
				return fmt.Errorf("docker_cli: %s needs a value", a)
			}
			i++
		case serviceNameRe.MatchString(a):
		default:
			return fmt.Errorf("docker_cli: argument %q not allowed", a)
		}
	}
	return nil
}
