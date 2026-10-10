package handlers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dplaned/internal/security"
)

// stacksEnv: a temporary stacks directory, docker "installed", compose faked.
func stacksEnv(t *testing.T, answers map[string]func([]string) ([]byte, error)) (*StackHandler, string, *fakeCmds) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("compose paths are checked as POSIX paths")
	}
	db := testDB(t) // gitops commit hooks run after changes
	dir := t.TempDir()
	prevDir, prevCheck, prevRoots := defaultStacksDir, checkBinary, security.ComposeRoots
	defaultStacksDir = dir
	checkBinary = func(string) error { return nil }
	security.ComposeRoots = append(append([]string{}, prevRoots...), dir+"/")
	t.Cleanup(func() { defaultStacksDir, checkBinary, security.ComposeRoots = prevDir, prevCheck, prevRoots })
	return NewStackHandler(db), dir, fakeCommands(t, answers)
}

const testCompose = "services:\n  web:\n    image: nginx:alpine\n"

func TestDeployStack(t *testing.T) {
	h, dir, cmds := stacksEnv(t, nil)
	for name, body := range map[string]map[string]any{
		"bad name":   {"name": "../etc", "yaml": testCompose},
		"dot name":   {"name": "my.stack", "yaml": testCompose},
		"no yaml":    {"name": "web"},
		"no service": {"name": "web", "yaml": "version: '3'\n"},
	} {
		if r := call(t, h.DeployStack, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	r := call(t, h.DeployStack, req{method: "POST", body: map[string]any{"name": "Web", "yaml": testCompose, "env": "PORT=8080\n"}})
	if !r.ok() {
		t.Fatalf("deploy: %s", r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "web", "docker-compose.yml"))
	env, _ := os.ReadFile(filepath.Join(dir, "web", ".env"))
	if !strings.Contains(string(b), "nginx:alpine") || string(env) != "PORT=8080\n" {
		t.Errorf("files: %q %q", b, env)
	}
	if !cmds.ran("docker_compose", "compose", "--project-directory") {
		t.Errorf("compose up not run: %v", cmds.keys())
	}
}

func TestDeployStackComposeFailure(t *testing.T) {
	h, _, _ := stacksEnv(t, map[string]func([]string) ([]byte, error){"docker_compose": fail("pull access denied for nginx")})
	r := call(t, h.DeployStack, req{method: "POST", body: map[string]any{"name": "web", "yaml": testCompose}})
	if r.ok() || !strings.Contains(r.raw, "pull access denied") {
		t.Errorf("a failed compose up must be reported with its output: %s", r)
	}
}

func TestDeleteStackKeepsData(t *testing.T) {
	h, dir, cmds := stacksEnv(t, nil)
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(filepath.Join(stack, "data"), 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	_ = os.WriteFile(filepath.Join(stack, "data", "db.sqlite"), []byte("x"), 0640)

	r := call(t, h.DeleteStack, req{method: "DELETE", path: "/api/docker/stacks?name=app"})
	if !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if !cmds.ran("docker_compose", "compose", "--project-directory", stack) && !cmds.ran("docker_compose", "compose", "--project-directory", filepath.ToSlash(stack)) {
		t.Errorf("compose down not run: %v", cmds.keys())
	}
	if _, err := os.Stat(filepath.Join(stack, "data", "db.sqlite")); err != nil {
		t.Error("the stack's data was deleted without purge")
	}
	if _, err := os.Stat(filepath.Join(stack, "docker-compose.yml")); err == nil {
		t.Error("compose file not removed")
	}
	if !strings.Contains(r.raw, "kept") {
		t.Errorf("the answer must say data was kept: %s", r)
	}
}

func TestDeleteStackPurge(t *testing.T) {
	h, dir, _ := stacksEnv(t, nil)
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(filepath.Join(stack, "data"), 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	if r := call(t, h.DeleteStack, req{method: "DELETE", path: "/api/docker/stacks?name=app&purge=true"}); !r.ok() {
		t.Fatalf("purge: %s", r)
	}
	if _, err := os.Stat(stack); err == nil {
		t.Error("folder kept despite purge")
	}
}

// If compose down fails, nothing is removed: the containers would keep
// running with no compose file left to manage them.
func TestDeleteStackDownFailure(t *testing.T) {
	h, dir, _ := stacksEnv(t, map[string]func([]string) ([]byte, error){"docker_compose": fail("Cannot connect to the Docker daemon")})
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(stack, 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	r := call(t, h.DeleteStack, req{method: "DELETE", path: "/api/docker/stacks?name=app"})
	if r.ok() {
		t.Fatalf("delete despite failed down: %s", r)
	}
	if _, err := os.Stat(filepath.Join(stack, "docker-compose.yml")); err != nil {
		t.Error("compose file removed although the stack could not be stopped")
	}
	if r := call(t, h.DeleteStack, req{method: "DELETE", path: "/api/docker/stacks?name=app&force=true"}); !r.ok() {
		t.Errorf("forced delete: %s", r)
	}
}

func TestStackAction(t *testing.T) {
	h, dir, cmds := stacksEnv(t, nil)
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(stack, 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	if r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "app", "action": "rm -rf"}}); r.code != 400 {
		t.Errorf("bad action: %s", r)
	}
	if r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "nope", "action": "stop"}}); r.code != 404 {
		t.Errorf("unknown stack: %s", r)
	}
	if r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "app", "action": "stop"}}); !r.ok() {
		t.Fatalf("stop: %s", r)
	}
	found := false
	for _, k := range cmds.keys() {
		if strings.HasPrefix(k, "docker_compose compose") && strings.HasSuffix(k, " stop") {
			found = true
		}
	}
	if !found {
		t.Errorf("compose stop not run: %v", cmds.keys())
	}
}

// update = pull, then up -d (recreates what changed).
func TestStackUpdate(t *testing.T) {
	h, dir, cmds := stacksEnv(t, nil)
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(stack, 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	if r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "app", "action": "update"}}); !r.ok() {
		t.Fatalf("update: %s", r)
	}
	var seq []string
	for _, k := range cmds.keys() {
		if strings.HasPrefix(k, "docker_compose compose") {
			seq = append(seq, k[strings.LastIndex(k, ".yml ")+5:])
		}
	}
	if len(seq) != 2 || seq[0] != "pull" || seq[1] != "up -d --remove-orphans" {
		t.Errorf("update ran %q, want pull then up", seq)
	}
}
