package dockerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ImageInfo is the part of an image inspect the update logic needs.
type ImageInfo struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"` // "repo@sha256:..." of the manifest it was pulled by
}

// imageRefRe: a registry/repository[:tag][@digest] reference. Slashes are
// part of it (the distribution route takes the name unescaped).
var imageRefRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/@-]{0,254}$`)

// InspectImage returns an image by reference or ID.
func (c *Client) InspectImage(ctx context.Context, ref string) (*ImageInfo, error) {
	resp, err := c.get(ctx, "/images/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return nil, fmt.Errorf("docker image inspect: %w", err)
	}
	var img ImageInfo
	if err := decodeJSON(resp, &img); err != nil {
		return nil, fmt.Errorf("docker image inspect %s: %w", ref, err)
	}
	return &img, nil
}

// RegistryDigest asks the registry, through the engine (and its registry
// credentials), for the digest a reference currently points to, without
// pulling: GET /distribution/{name}/json, as Portainer does for its update
// badges. For a multi-platform image this is the index digest, which is
// also what the engine records in RepoDigests on pull.
func (c *Client) RegistryDigest(ctx context.Context, ref string) (string, error) {
	if !imageRefRe.MatchString(ref) || strings.Contains(ref, "..") {
		return "", fmt.Errorf("invalid image reference %q", ref)
	}
	resp, err := c.get(ctx, "/distribution/"+ref+"/json", nil)
	if err != nil {
		return "", fmt.Errorf("registry lookup %s: %w", ref, err)
	}
	var d struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"Descriptor"`
	}
	if err := decodeJSON(resp, &d); err != nil {
		return "", fmt.Errorf("registry lookup %s: %w", ref, err)
	}
	if d.Descriptor.Digest == "" {
		return "", fmt.Errorf("registry lookup %s: no digest", ref)
	}
	return d.Descriptor.Digest, nil
}

// TagImage points repo:tag at the image id (used to go back to the previous
// image of a service after a failed update).
func (c *Client) TagImage(ctx context.Context, id, repo, tag string) error {
	resp, err := c.post(ctx, "/images/"+url.PathEscape(id)+"/tag", url.Values{"repo": {repo}, "tag": {tag}})
	if err != nil {
		return fmt.Errorf("docker tag: %w", err)
	}
	return expectOK(resp)
}

// ProjectContainers lists the containers (running or not) of a compose project.
func (c *Client) ProjectContainers(ctx context.Context, project string) ([]Container, error) {
	f, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + project}})
	resp, err := c.get(ctx, "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}})
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var cs []Container
	if err := decodeJSON(resp, &cs); err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	return cs, nil
}

// SplitImageRef splits "registry/repo:tag" into repo and tag (default
// "latest"); a reference pinned by digest has no tag ("").
func SplitImageRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ""
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// HasDigest reports whether a RepoDigests entry list contains digest.
func HasDigest(repoDigests []string, digest string) bool {
	for _, d := range repoDigests {
		if strings.HasSuffix(d, "@"+digest) {
			return true
		}
	}
	return false
}
