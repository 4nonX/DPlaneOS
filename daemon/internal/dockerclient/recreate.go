package dockerclient

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Recreation: a container keeps the image it was created from, so updating
// it means creating a new container from the old one's configuration with
// the new image. The old one is kept (renamed) until the caller commits or
// rolls back.

// Recreated is a container replaced by Recreate.
type Recreated struct {
	Name  string // the container's name (now the new container)
	NewID string
	OldID string // the previous container, stopped and renamed
	old   string // its temporary name
}

// rawInspect: the parts of an inspect that are fed back into a create.
type rawInspect struct {
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	Config          map[string]any `json:"Config"`
	HostConfig      map[string]any `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]map[string]any `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ComposeProject returns the compose project a container belongs to ("" if none).
func (c *Client) ComposeProject(ctx context.Context, name string) (string, error) {
	in, err := c.rawInspect(ctx, name)
	if err != nil {
		return "", err
	}
	if labels, ok := in.Config["Labels"].(map[string]any); ok {
		if p, ok := labels["com.docker.compose.project"].(string); ok {
			return p, nil
		}
	}
	return "", nil
}

func (c *Client) rawInspect(ctx context.Context, name string) (*rawInspect, error) {
	resp, err := c.get(ctx, "/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var in rawInspect
	if err := decodeJSON(resp, &in); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	return &in, nil
}

func (c *Client) rename(ctx context.Context, id, name string) error {
	resp, err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}})
	if err != nil {
		return fmt.Errorf("docker rename: %w", err)
	}
	return expectOK(resp)
}

// endpointForCreate keeps the user-set parts of a network endpoint (static
// addresses, aliases, links); runtime fields (IDs, assigned addresses) are
// dropped.
func endpointForCreate(ep map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"IPAMConfig", "Links", "Aliases", "DriverOpts", "MacAddress"} {
		if v, ok := ep[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// buildCreateBody: the old container's Config and HostConfig with the new
// image, attached to its primary network (the others are connected after
// creation; older engines accept a single network at create).
func buildCreateBody(in *rawInspect, image string) (body map[string]any, primary string, others map[string]map[string]any) {
	body = map[string]any{}
	for k, v := range in.Config {
		body[k] = v
	}
	body["Image"] = image
	// A hostname Docker generated is the old container's short ID: let the
	// new container get its own.
	if h, ok := body["Hostname"].(string); ok && len(in.ID) >= 12 && h == in.ID[:12] {
		delete(body, "Hostname")
	}
	body["HostConfig"] = in.HostConfig

	mode, _ := in.HostConfig["NetworkMode"].(string)
	others = map[string]map[string]any{}
	for name, ep := range in.NetworkSettings.Networks {
		if name == mode || (primary == "" && (mode == "default" || mode == "") && name == "bridge") {
			primary = name
			continue
		}
		others[name] = ep
	}
	if primary != "" {
		if ep, ok := in.NetworkSettings.Networks[primary]; ok {
			body["NetworkingConfig"] = map[string]any{
				"EndpointsConfig": map[string]any{primary: endpointForCreate(ep)},
			}
		}
	}
	return body, primary, others
}

// Recreate replaces the (stopped) container name with a new container from
// image, with the same configuration, and starts it. The old container is
// renamed and left stopped: Commit removes it, Rollback restores it.
func (c *Client) Recreate(ctx context.Context, name, image string) (*Recreated, error) {
	in, err := c.rawInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	clean := name
	if len(in.Name) > 1 {
		clean = in.Name[1:]
	}
	r := &Recreated{Name: clean, OldID: in.ID, old: fmt.Sprintf("%s-pre-update-%d", clean, time.Now().Unix())}

	body, _, others := buildCreateBody(in, image)
	if err := c.rename(ctx, in.ID, r.old); err != nil {
		return nil, err
	}
	resp, err := c.postJSON(ctx, "/containers/create", url.Values{"name": {clean}}, body)
	if err != nil {
		_ = c.rename(ctx, in.ID, clean)
		return nil, fmt.Errorf("docker create: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := decodeJSON(resp, &created); err != nil {
		_ = c.rename(ctx, in.ID, clean)
		return nil, fmt.Errorf("docker create: %w", err)
	}
	r.NewID = created.ID
	for netName, ep := range others {
		resp, err := c.postJSON(ctx, "/networks/"+url.PathEscape(netName)+"/connect", nil, map[string]any{
			"Container": created.ID, "EndpointConfig": endpointForCreate(ep),
		})
		if err == nil {
			err = expectOK(resp)
		}
		if err != nil {
			_ = c.Rollback(context.Background(), r)
			return nil, fmt.Errorf("connect %s: %w", netName, err)
		}
	}
	if err := c.Start(ctx, created.ID); err != nil {
		return r, err // the caller rolls back
	}
	return r, nil
}

// Commit removes the old container (its volumes stay).
func (c *Client) Commit(ctx context.Context, r *Recreated) error {
	return c.Remove(ctx, r.OldID, true, false)
}

// Rollback removes the new container and restores and starts the old one.
func (c *Client) Rollback(ctx context.Context, r *Recreated) error {
	if r.NewID != "" {
		if err := c.Remove(ctx, r.NewID, true, false); err != nil {
			return err
		}
	}
	if err := c.rename(ctx, r.OldID, r.Name); err != nil {
		return err
	}
	return c.Start(ctx, r.OldID)
}
