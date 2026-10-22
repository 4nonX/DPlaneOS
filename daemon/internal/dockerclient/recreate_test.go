package dockerclient

import "testing"

func TestBuildCreateBody(t *testing.T) {
	in := &rawInspect{
		ID:   "0123456789abcdef",
		Name: "/plex",
		Config: map[string]any{
			"Image": "plex:1.0", "Hostname": "0123456789ab", "Env": []any{"TZ=UTC"},
		},
		HostConfig: map[string]any{"NetworkMode": "media", "Binds": []any{"/mnt/tank/plex:/config"}},
	}
	in.NetworkSettings.Networks = map[string]map[string]any{
		"media":   {"NetworkID": "n1", "IPAddress": "172.20.0.5", "Aliases": []any{"plex"}, "IPAMConfig": map[string]any{"IPv4Address": "172.20.0.5"}},
		"backend": {"NetworkID": "n2"},
	}
	body, primary, others := buildCreateBody(in, nil, "plex:2.0")
	if body["Image"] != "plex:2.0" {
		t.Errorf("image: %v", body["Image"])
	}
	if _, ok := body["Hostname"]; ok {
		t.Error("generated hostname (old short ID) carried over")
	}
	if body["HostConfig"].(map[string]any)["Binds"] == nil || body["Env"] == nil {
		t.Error("configuration lost")
	}
	if primary != "media" || len(others) != 1 || others["backend"] == nil {
		t.Errorf("networks: primary %q, others %v", primary, others)
	}
	ep := body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)["media"].(map[string]any)
	if ep["IPAMConfig"] == nil || ep["Aliases"] == nil || ep["NetworkID"] != nil || ep["IPAddress"] != nil {
		t.Errorf("endpoint: %v", ep)
	}
	// A hostname the user set is kept.
	in.Config["Hostname"] = "plexbox"
	if body, _, _ := buildCreateBody(in, nil, "plex:2.0"); body["Hostname"] != "plexbox" {
		t.Error("user hostname dropped")
	}
}

// Reproduced on Engine 29: v1 image (ENV APP_VERSION=1, WORKDIR /v1, CMD
// "echo running v1"), container with MY_SETTING=kept, updated to v2. The
// copied config kept APP_VERSION=1, the v1 command and /v1, and the
// "updated" container went on running v1. Only the container's own settings
// may be carried over.
func TestWithoutImageDefaults(t *testing.T) {
	imageV1 := map[string]any{
		"Env":          []any{"PATH=/usr/bin:/bin", "APP_VERSION=1"},
		"Cmd":          []any{"sh", "-c", "echo running v1; sleep 3600"},
		"WorkingDir":   "/v1",
		"Labels":       map[string]any{"org.opencontainers.image.version": "1", "maintainer": "x"},
		"ExposedPorts": map[string]any{"80/tcp": map[string]any{}},
		"Volumes":      map[string]any{"/data": map[string]any{}},
	}
	container := map[string]any{
		"Env":          []any{"MY_SETTING=kept", "PATH=/usr/bin:/bin", "APP_VERSION=1"},
		"Cmd":          []any{"sh", "-c", "echo running v1; sleep 3600"},
		"WorkingDir":   "/v1",
		"Labels":       map[string]any{"org.opencontainers.image.version": "1", "maintainer": "x", "dplaneos.icon": "plex"},
		"ExposedPorts": map[string]any{"80/tcp": map[string]any{}, "9000/tcp": map[string]any{}},
		"Volumes":      map[string]any{"/data": map[string]any{}},
		"Hostname":     "app",
	}
	got := withoutImageDefaults(container, imageV1)
	if env := got["Env"].([]any); len(env) != 1 || env[0] != "MY_SETTING=kept" {
		t.Errorf("env: %v", env)
	}
	for _, k := range []string{"Cmd", "WorkingDir"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s from the old image carried over: %v", k, got[k])
		}
	}
	if l := got["Labels"].(map[string]any); len(l) != 1 || l["dplaneos.icon"] != "plex" {
		t.Errorf("labels: %v", l)
	}
	if p := got["ExposedPorts"].(map[string]any); len(p) != 1 || p["9000/tcp"] == nil {
		t.Errorf("ports: %v", p)
	}
	if v := got["Volumes"].(map[string]any); len(v) != 0 {
		t.Errorf("volumes: %v", v)
	}
	if got["Hostname"] != "app" {
		t.Error("container setting lost")
	}
	if _, ok := container["WorkingDir"]; !ok {
		t.Error("the inspect itself must not be modified")
	}

	// A command set on the container (not the image's) stays.
	container["Cmd"] = []any{"sh", "-c", "custom"}
	if got := withoutImageDefaults(container, imageV1); got["Cmd"] == nil {
		t.Error("the container's own command was dropped")
	}
	// Without the old image's config nothing is removed.
	if got := withoutImageDefaults(container, nil); got["WorkingDir"] != "/v1" {
		t.Error("nothing may be removed without the image config")
	}
}
