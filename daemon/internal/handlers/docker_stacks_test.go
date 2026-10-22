package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dplaned/internal/dockerclient"
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
	// Checked with compose config before the files are written, then started
	// under its own project name.
	if !cmds.ran("docker_compose", "compose", "-p", "web", "--project-directory", filepath.Join(dir, "web"), "-f", filepath.Join(dir, "web", ".dplaneos-candidate.yml")) {
		t.Errorf("compose config not run: %v", cmds.keys())
	}
	if !cmds.ran("docker_compose", "compose", "-p", "web", "--project-directory", filepath.Join(dir, "web"), "-f", filepath.Join(dir, "web", "docker-compose.yml"), "up", "-d", "--remove-orphans") {
		t.Errorf("compose up not run: %v", cmds.keys())
	}
	if _, err := os.Stat(filepath.Join(dir, "web", ".dplaneos-candidate.yml")); err == nil {
		t.Error("candidate file left behind")
	}
	// A second deploy under the same name is refused (edit the stack instead).
	if r := call(t, h.DeployStack, req{method: "POST", body: map[string]any{"name": "web", "yaml": testCompose}}); r.code != 409 {
		t.Errorf("deploy over an existing stack: %s", r)
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
	if !cmds.ran("docker_compose", "compose", "-p", "app", "--project-directory", stack, "-f", filepath.Join(stack, "docker-compose.yml"), "down", "--remove-orphans") {
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

// fakeDockerAPI serves the Docker API calls an update makes for a stack with
// one service "web" (nginx:alpine): the container runs image *running, the
// tag points at *tagged. crash makes the container keep restarting.
type fakeDockerAPI struct {
	mu              sync.Mutex
	running, tagged string
	crash           bool
	tagCalls        []string
}

func (f *fakeDockerAPI) serve(t *testing.T) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ApiVersion":"1.47","MinAPIVersion":"1.24"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/containers/json"):
			fmt.Fprint(w, `[{"Id":"c1","Labels":{"com.docker.compose.project":"app","com.docker.compose.service":"web"},"Mounts":[]}]`)
		case strings.HasSuffix(p, "/containers/c1/json"):
			state := `{"Running":true,"Restarting":false,"StartedAt":"2026-10-18T00:00:00Z","ExitCode":0}`
			if f.crash && f.running != "sha256:old" {
				state = `{"Running":false,"Restarting":true,"StartedAt":"2026-10-18T00:00:00Z","ExitCode":1}`
			}
			fmt.Fprintf(w, `{"Id":"c1","Image":%q,"RestartCount":0,"Config":{"Image":"nginx:alpine"},"State":%s,"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"}}}`, f.running, state)
		case strings.Contains(p, "/images/") && strings.HasSuffix(p, "/json"):
			fmt.Fprintf(w, `{"Id":%q,"RepoTags":["nginx:alpine"],"RepoDigests":["nginx@sha256:d1"]}`, f.tagged)
		case strings.Contains(p, "/images/") && strings.HasSuffix(p, "/tag"):
			f.tagCalls = append(f.tagCalls, p+"?"+r.URL.RawQuery)
			f.tagged = "sha256:old"
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l) //nolint:errcheck
	prev, prevStable := dockerclient.SocketPath, dockerclient.StableFor
	dockerclient.SocketPath, dockerclient.StableFor = sock, 50*time.Millisecond
	t.Cleanup(func() { srv.Close(); dockerclient.SocketPath, dockerclient.StableFor = prev, prevStable })
}

// compose fakes "pull" (the tag moves to the new image) and "up" (the
// container is recreated from what the tag points at).
func (f *fakeDockerAPI) compose() map[string]func([]string) ([]byte, error) {
	return map[string]func([]string) ([]byte, error){"docker_compose": func(args []string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case slices.Contains(args, "pull"):
			f.tagged = "sha256:new"
		case slices.Contains(args, "up"):
			f.running = f.tagged
		}
		return nil, nil
	}}
}

// update = pull, then up -d for what changed, then wait until it is up.
func TestStackUpdate(t *testing.T) {
	f := &fakeDockerAPI{running: "sha256:old", tagged: "sha256:old"}
	f.serve(t)
	h, dir, cmds := stacksEnv(t, f.compose())
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(stack, 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "app", "action": "update"}})
	if !r.ok() {
		t.Fatalf("update: %s", r)
	}
	var seq []string
	for _, k := range cmds.keys() {
		if strings.HasPrefix(k, "docker_compose compose -p app") {
			seq = append(seq, k[strings.LastIndex(k, ".yml ")+5:])
		}
	}
	if len(seq) != 2 || seq[0] != "pull" || seq[1] != "up -d --remove-orphans" {
		t.Errorf("update ran %q, want pull then up", seq)
	}
	if f.running != "sha256:new" || len(f.tagCalls) != 0 {
		t.Errorf("running %s, re-tags %v", f.running, f.tagCalls)
	}

	// Nothing new: no recreate.
	cmds.reset()
	if r := call(t, h.StackAction, req{method: "POST", body: map[string]any{"name": "app", "action": "update"}}); !r.ok() {
		t.Fatalf("second update: %s", r)
	}
	for _, k := range cmds.keys() {
		if strings.HasSuffix(k, "up -d --remove-orphans") {
			t.Error("recreated although the image did not change")
		}
	}
}

// A new image whose container does not come up: the tag goes back to the
// image that ran and the stack is recreated from it.
func TestStackUpdateRollsBack(t *testing.T) {
	f := &fakeDockerAPI{running: "sha256:old", tagged: "sha256:old", crash: true}
	f.serve(t)
	_, dir, _ := stacksEnv(t, f.compose())
	stack := filepath.Join(dir, "app")
	_ = os.MkdirAll(stack, 0750)
	_ = os.WriteFile(filepath.Join(stack, "docker-compose.yml"), []byte(testCompose), 0640)
	res := updateStack(context.Background(), nil, "app", StackUpdateOptions{HealthTimeout: 2 * time.Second})
	if !res.RolledBack || res.Error == "" || f.running != "sha256:old" {
		t.Errorf("result %+v, running %s", res, f.running)
	}
	if len(f.tagCalls) != 1 || !strings.Contains(f.tagCalls[0], "/images/sha256:old/tag") || !strings.Contains(f.tagCalls[0], "tag=alpine") {
		t.Errorf("re-tag: %v", f.tagCalls)
	}
	if res.FailedImages["nginx:alpine"] != "sha256:d1" {
		t.Errorf("failed digest not recorded: %v", res.FailedImages)
	}
}
