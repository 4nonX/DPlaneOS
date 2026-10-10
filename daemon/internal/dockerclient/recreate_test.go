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
	body, primary, others := buildCreateBody(in, "plex:2.0")
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
	if body, _, _ := buildCreateBody(in, "plex:2.0"); body["Hostname"] != "plexbox" {
		t.Error("user hostname dropped")
	}
}
