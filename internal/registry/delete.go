package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// DeleteManifest removes a manifest by digest from a repository.
//
// Per registry-manager's AGENTS.md, "DELETE only works on digests, not tags.
// A 400 DIGEST_INVALID response on tag-formatted refs is expected, not a bug."
//
// The V2 spec mandates 202 Accepted on success; registry-manager treats both
// 200 and 202 as success (some registries return 200). We accept any 2xx.
//
// Caller is responsible for confirming that the digest isn't shared by other
// tags the user wanted to keep; we return the affected tags so the UI can
// render a confirmation list.
func (c *Client) DeleteManifest(ctx context.Context, repo, digest string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("registry: repo is required")
	}
	if digest == "" {
		return nil, fmt.Errorf("registry: digest is required")
	}
	path := fmt.Sprintf("/v2/%s/manifests/%s", escapeRepo(repo), url.PathEscape(digest))
	resp, _, err := c.doRequest(ctx, http.MethodDelete, path, "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{
			Status:  resp.StatusCode,
			Code:    "DELETE_FAILED",
			Message: fmt.Sprintf("DELETE %s returned %d", path, resp.StatusCode),
			URL:     path,
		}
	}
	return c.tagsForDigest(ctx, repo, digest)
}

// tagsForDigest lists all tags in repo that currently point at digest.
//
// Used by the delete handler to render "this digest is shared by these tags,
// deleting will affect them all" before the user clicks the red button.
//
// It's a best-effort scan: any tag whose manifest fetch errors out is
// omitted from the list. The UI shows what we found; if some tags are
// missing, the user will see them disappear anyway post-delete.
func (c *Client) tagsForDigest(ctx context.Context, repo, digest string) ([]string, error) {
	tags, err := c.ListTags(ctx, repo)
	if err != nil {
		return nil, err
	}
	var hits []string
	for _, t := range tags {
		m, err := c.GetManifest(ctx, repo, t)
		if err != nil {
			continue
		}
		if m.Digest == digest {
			hits = append(hits, t)
		}
	}
	return hits, nil
}
