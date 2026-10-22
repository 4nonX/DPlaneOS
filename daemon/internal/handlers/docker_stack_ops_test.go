package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dplaned/internal/dockerclient"
	"dplaned/internal/security"
)

type fakeImages struct {
	images  map[string]*dockerclient.ImageInfo // by ID
	remote  map[string]string                  // ref -> digest
	lookups int
}

func (f *fakeImages) InspectImage(_ context.Context, ref string) (*dockerclient.ImageInfo, error) {
	if i, ok := f.images[ref]; ok {
		return i, nil
	}
	return nil, fmt.Errorf("no such image %s", ref)
}

func (f *fakeImages) RegistryDigest(_ context.Context, ref string) (string, error) {
	f.lookups++
	if d, ok := f.remote[ref]; ok {
		return d, nil
	}
	return "", fmt.Errorf("unauthorized")
}

func TestCheckStackImages(t *testing.T) {
	ic := &fakeImages{
		images: map[string]*dockerclient.ImageInfo{
			"sha256:app":    {ID: "sha256:app", RepoDigests: []string{"lscr.io/app@sha256:old"}},
			"sha256:db":     {ID: "sha256:db", RepoDigests: []string{"postgres@sha256:same"}},
			"sha256:local":  {ID: "sha256:local"},
			"sha256:worker": {ID: "sha256:worker", RepoDigests: []string{"lscr.io/app@sha256:old"}},
		},
		remote: map[string]string{"lscr.io/app:latest": "sha256:new", "postgres:16": "sha256:same"},
	}
	svcs := []stackService{
		{Service: "app", ImageRef: "lscr.io/app:latest", ImageID: "sha256:app"},
		{Service: "worker", ImageRef: "lscr.io/app:latest", ImageID: "sha256:worker"},
		{Service: "db", ImageRef: "postgres:16", ImageID: "sha256:db"},
		{Service: "custom", ImageRef: "my/build:dev", ImageID: "sha256:local"},
		{Service: "pinned", ImageRef: "redis@sha256:abc", ImageID: "sha256:x"},
		{Service: "private", ImageRef: "ghcr.io/me/x:1", ImageID: "sha256:app"},
	}
	got := checkStackImages(context.Background(), ic, svcs, map[string]string{"lscr.io/app:latest": "sha256:new"})
	by := map[string]ServiceUpdate{}
	for _, u := range got {
		by[u.Service] = u
	}
	if !by["app"].UpdateAvailable || !by["app"].FailedBefore {
		t.Errorf("app: %+v (new digest, which failed before)", by["app"])
	}
	if by["db"].UpdateAvailable {
		t.Errorf("db is current: %+v", by["db"])
	}
	for _, s := range []string{"custom", "pinned", "private"} {
		if by[s].UpdateAvailable || by[s].Note == "" {
			t.Errorf("%s must be reported as not checkable: %+v", s, by[s])
		}
	}
	if ic.lookups != 3 { // app (once for two services), postgres, ghcr
		t.Errorf("registry asked %d times, want once per reference (3)", ic.lookups)
	}
	if autoUpdateWanted(StackUpdates{Services: []ServiceUpdate{by["app"], by["db"]}}) {
		t.Error("auto-update must not retry a digest that was rolled back")
	}
	if !autoUpdateWanted(StackUpdates{Services: []ServiceUpdate{{UpdateAvailable: true}}}) {
		t.Error("a new digest must be auto-updated")
	}
}

// Engine 29 (containerd image store): once a tag moves on, the image a
// service still runs has no RepoDigests (seen in the lab after `docker pull`).
func TestCheckStackImagesAfterTagMoved(t *testing.T) {
	ic := &fakeImages{
		images: map[string]*dockerclient.ImageInfo{
			"sha256:v1":        {ID: "sha256:v1"}, // digests gone
			"app:latest":       {ID: "sha256:v2", RepoDigests: []string{"app@sha256:d2"}},
			"sha256:same":      {ID: "sha256:same"},
			"db:16":            {ID: "sha256:same", RepoDigests: []string{"db@sha256:d16"}},
			"sha256:localonly": {ID: "sha256:localonly"},
			"mine:dev":         {ID: "sha256:localonly"},
		},
		remote: map[string]string{"db:16": "sha256:d16"},
	}
	got := checkStackImages(context.Background(), ic, []stackService{
		{Service: "app", ImageRef: "app:latest", ImageID: "sha256:v1"},
		{Service: "db", ImageRef: "db:16", ImageID: "sha256:same"},
		{Service: "mine", ImageRef: "mine:dev", ImageID: "sha256:localonly"},
	}, nil)
	if !got[0].UpdateAvailable || got[0].RemoteDigest != "sha256:d2" || !strings.Contains(got[0].Note, "already pulled") {
		t.Errorf("pulled but not applied: %+v", got[0])
	}
	if got[1].UpdateAvailable || got[1].Note != "" {
		t.Errorf("tag's digests stand in for the running image's: %+v", got[1])
	}
	if !strings.Contains(got[2].Note, "built locally") {
		t.Errorf("locally built: %+v", got[2])
	}
}

func TestLockStack(t *testing.T) {
	unlock, ok := lockStack("media")
	if !ok {
		t.Fatal("free stack not locked")
	}
	if _, ok := lockStack("media"); ok {
		t.Error("second operation on a busy stack allowed")
	}
	if u2, ok := lockStack("other"); !ok {
		t.Error("another stack must not be blocked")
	} else {
		u2()
	}
	unlock()
	if u, ok := lockStack("media"); !ok {
		t.Error("lock not released")
	} else {
		u()
	}
}

// env nil keeps the .env, "" removes it; restore puts both back.
func TestWriteStackFilesAndRestore(t *testing.T) {
	dir := t.TempDir()
	env := "A=1"
	if err := writeStackFiles(dir, []byte("services: {}"), &env); err != nil {
		t.Fatal(err)
	}
	prev, err := readStackFiles(dir)
	if err != nil || !prev.hasEnv {
		t.Fatalf("read: %v %+v", err, prev)
	}
	if err := writeStackFiles(dir, []byte("services: {x: {}}"), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); string(b) != "A=1" {
		t.Errorf("env nil must keep .env, got %q", b)
	}
	empty := ""
	writeStackFiles(dir, []byte("services: {x: {}}"), &empty) //nolint
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Error(`env "" must remove .env`)
	}
	if err := prev.restore(dir); err != nil {
		t.Fatal(err)
	}
	y, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	e, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(y) != "services: {}" || string(e) != "A=1" {
		t.Errorf("restore: %q %q", y, e)
	}
}

func TestRecordStackUpdate(t *testing.T) {
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	recordStackUpdate("media", StackUpdateResult{Error: "x not up", RolledBack: true,
		FailedImages: map[string]string{"app:latest": "sha256:bad"}})
	if f := loadStackUpdateState()["media"].Failed; f["app:latest"] != "sha256:bad" {
		t.Errorf("failure not recorded: %v", f)
	}
	recordStackUpdate("media", StackUpdateResult{UpToDate: true})
	if f := loadStackUpdateState()["media"].Failed; f["app:latest"] != "sha256:bad" {
		t.Error("an up-to-date check must not clear a failure")
	}
	recordStackUpdate("media", StackUpdateResult{Updated: []string{"app"}})
	if f := loadStackUpdateState()["media"].Failed; len(f) != 0 {
		t.Errorf("a working update clears the failures: %v", f)
	}
	if o := stackUpdateOptionsFor("media"); !o.Snapshot || o.RollbackData {
		t.Errorf("default options: %+v", o)
	}
}

func TestStackComposeArgsAllowed(t *testing.T) {
	dir := "/var/lib/dplaneos/stacks/media"
	for _, args := range [][]string{
		stackComposeArgs("media", dir, "ps", "--all", "--format", "json"),
		stackComposeArgs("media", dir, "up", "-d", "--remove-orphans"),
		{"compose", "-p", "media", "--project-directory", dir, "-f", dir + "/.dplaneos-candidate.yml",
			"--env-file", dir + "/.dplaneos-candidate.env", "config", "-q"},
	} {
		if err := security.ValidateCommand("docker_compose", args); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"compose", "-p", "Bad Name", "up"},
		{"compose", "--env-file", "/etc/shadow", "config"},
		{"compose", "-p", "-x", "up"},
	} {
		if err := security.ValidateCommand("docker_compose", args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestGitStackProject(t *testing.T) {
	for in, want := range map[string]string{"Media Server": "git-media-server", "home": "git-home", "-x-": "git-x"} {
		if got := gitStackProject(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestOrphanSandboxBases(t *testing.T) {
	out := strings.Join([]string{
		"tank/data@sandbox-base-a\ttank/sandboxes/a",
		"tank/data@sandbox-base-b\t-",
		"tank/data@sandbox-base-c\t",
		"tank/data@auto-daily-1\t-",
	}, "\n")
	got := orphanSandboxBases(out)
	if strings.Join(got, ",") != "tank/data@sandbox-base-b,tank/data@sandbox-base-c" {
		t.Errorf("orphans: %v", got)
	}
}
