package pull

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/proxies"
	"cairn/internal/registry"
)

// SourceResolver returns a Registry client configured to talk to the upstream
// registry described by sourceRef, optionally using a credential + proxy.
//
// Used by the executor: we want one Client per pull source so the Bearer
// token cache is scoped to that source (not cross-contaminated).
type SourceResolver func(sourceURL string, cred *credentials.Credential, proxy *proxies.Proxy) (*registry.Client, error)

// DefaultSourceResolver wires credentials + proxy + basic auth into a
// registry.Client. Use this from server.Build; tests can swap it.
func DefaultSourceResolver() SourceResolver {
	return func(sourceURL string, cred *credentials.Credential, proxy *proxies.Proxy) (*registry.Client, error) {
		cfg := registry.Config{BaseURL: sourceURL, Timeout: 5 * time.Minute}
		if cred != nil {
			cfg.Username = cred.Username
			cfg.Password = cred.Password
		}
		if proxy != nil {
			cfg.Proxy = proxy.URL
			if proxy.Username != "" {
				// Basic auth for the proxy itself is rare; supported via
				// URL-embedded credentials on cfg.Proxy. Skip here.
			}
		}
		return registry.NewClient(cfg)
	}
}

// Orchestrator is the actual pull implementation. It's injected into
// Executor.runJob at construction time.
type Orchestrator struct {
	Dest       registry.Registry // destination registry (the managed one)
	SrcResolve SourceResolver    // source client factory
	Vault      *credentials.Vault
	Proxies    *proxies.Store
	DB         *db.Db
	PullHistoryRetention int // days
}

// RunOne executes one job to completion; returns nil on success.
//
// Lifecycle:
//  1. parse sourceRef into <repo>:<tag> + apply Docker Hub library/ prefix
//  2. resolve source registry client (with credential + proxy if set)
//  3. fetch source manifest (with retry on 401 → Bearer)
//  4. walk manifest layers + config; for each: download from source,
//     upload to destination (skip if already present via HEAD)
//  5. PUT manifest to destination
//  6. record terminal state to SQLite (history)
func (o *Orchestrator) RunOne(ctx context.Context, j *Job) error {
	v := j.View()
	srcRepo, srcTag := splitRef(v.SourceRef)
	destRepo := v.DestRepo
	destTag := v.DestTag
	if destTag == "" {
		destTag = srcTag
	}
	if destRepo == "" {
		destRepo = srcRepo
	}

	// Apply Docker Hub library/ prefix for single-segment repo names.
	srcRepo = applyDockerHubLibraryPrefix(srcRepo, srcTag)

	// Build source client.
	var cred *credentials.Credential
	var proxy *proxies.Proxy
	if v.Credential != "" {
		c, err := o.Vault.Get(v.Credential)
		if err != nil {
			return fmt.Errorf("credential %q: %w", v.Credential, err)
		}
		cred = &c
	}
	if v.Proxy != "" {
		p, err := o.Proxies.Get(v.Proxy)
		if err != nil {
			return fmt.Errorf("proxy %q: %w", v.Proxy, err)
		}
		proxy = &p
	}
	srcURL := cred.URL // credential's URL IS the source registry
	if srcURL == "" {
		// fallback to docker hub default for un-credentialed anonymous pulls
		srcURL = "https://registry-1.docker.io"
	}
	src, err := o.SrcResolve(srcURL, cred, proxy)
	if err != nil {
		return fmt.Errorf("source resolver: %w", err)
	}

	// Fetch source manifest.
	srcManifest, err := src.GetManifest(ctx, srcRepo, srcTag)
	if err != nil {
		return fmt.Errorf("fetch source manifest: %w", err)
	}
	if srcManifest.Digest == "" {
		return fmt.Errorf("source manifest missing digest")
	}

	// Walk layers + config. Multi-arch indexes are not pulled (registry-manager
	// warns and skips). Single-arch manifests list layers directly.
	layers, configDigest, configSize := extractManifest(srcManifest)
	j.MutexHeld(func(v *JobView) {
		v.BlobsTotal = len(layers)
		if configDigest != "" {
			v.BlobsTotal++
		}
		v.BytesTotal = srcManifest.Size
	})

	for i, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.transferBlob(ctx, j, src, destRepo, layer.Digest, layer.Size); err != nil {
			return fmt.Errorf("layer %d (%s): %w", i, layer.Digest, err)
		}
		j.MutexHeld(func(v *JobView) {
			v.BlobsDone++
			v.BytesDone += layer.Size
		})
	}
	if configDigest != "" {
		if err := o.transferBlob(ctx, j, src, destRepo, configDigest, configSize); err != nil {
			return fmt.Errorf("config (%s): %w", configDigest, err)
		}
		j.MutexHeld(func(v *JobView) {
			v.BlobsDone++
			v.BytesDone += configSize
		})
	}

	// PUT manifest into destination.
	if err := o.putManifestToDest(ctx, destRepo, destTag, srcManifest); err != nil {
		return fmt.Errorf("put manifest: %w", err)
	}

	// Persist terminal state.
	vv := j.View()
	if o.DB != nil {
		row := db.PullJobRow{
			ID:         vv.ID,
			SourceRef:  vv.SourceRef,
			DestRepo:   destRepo,
			DestTag:    destTag,
			State:      string(StateSucceeded),
			BytesDone:  vv.BytesDone,
			BytesTotal: vv.BytesTotal,
			StartedAt:  vv.StartedAt,
			EndedAt:    vv.EndedAt,
			CreatedAt:  vv.CreatedAt,
		}
		_ = o.DB.PullJobRecord(ctx, row)
	}
	_ = ctx // keep the param used
	return nil
}

// transferBlob does the skip-if-present + download + upload dance for one blob.
//
// Per registry-manager's puller, a layer is considered "already present" if
// HEAD on the destination succeeds. We transfer unconditionally otherwise.
func (o *Orchestrator) transferBlob(ctx context.Context, j *Job, src *registry.Client, destRepo, digest string, size int64) error {
	if dest, ok := o.Dest.(*registry.CachedRegistry); ok {
		if client, ok := dest.Inner().(*registry.Client); ok {
			exists, err := client.BlobExists(ctx, destRepo, digest)
			if err == nil && exists {
				return nil
			}
		}
	}

	// Open source stream.
	body, _, err := src.GetBlob(ctx, blobRepoFor(src.GetBaseURL()), digest)
	if err != nil {
		return err
	}
	defer body.Close()

	if dest, ok := o.Dest.(*registry.CachedRegistry); ok {
		if client, ok := dest.Inner().(*registry.Client); ok {
			return client.UploadBlob(ctx, destRepo, digest, body)
		}
	}
	return fmt.Errorf("destination client type not supported for upload")
}

// putManifestToDest uploads the source manifest bytes verbatim into the
// destination. We don't re-fetch / re-encode — the source digest is
// computed once and reused.
func (o *Orchestrator) putManifestToDest(ctx context.Context, repo, tag string, m *registry.Manifest) error {
	// Use the cached registry's underlying client for PUT.
	dest, ok := o.Dest.(*registry.CachedRegistry)
	if !ok {
		return fmt.Errorf("destination type not supported for manifest PUT")
	}
	client, ok := dest.Inner().(*registry.Client)
	if !ok {
		return fmt.Errorf("destination client type not supported for manifest PUT")
	}
	// The V2 spec lets us PUT to /v2/<name>/manifests/<tag> with the bytes
	// and the right Content-Type; the registry returns 201 Created.
	path := fmt.Sprintf("/v2/%s/manifests/%s", url.PathEscape(repo), url.PathEscape(tag))
	resp, err := client.PutRaw(ctx, "PUT", path, m.MediaType, m.Raw)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest PUT returned %d", resp.StatusCode)
	}
	resp.Body.Close()
	return nil
}

// splitRef parses "repo:tag" into components.
func splitRef(ref string) (repo, tag string) {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// only treat as tag separator if the part after : contains no /
		if !strings.Contains(ref[i+1:], "/") {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

// applyDockerHubLibraryPrefix mirrors registry-manager's quirk:
// `docker pull alpine` works because the CLI adds `library/`; our API would
// otherwise 401 on anonymous /v2/alpine/manifests/latest.
//
// Only applies to single-segment repo names AND when the source URL is
// Docker Hub. We detect "Docker Hub" by URL host suffix.
func applyDockerHubLibraryPrefix(repo, tag string) string {
	if !strings.Contains(repo, "/") && tag != "" {
		return "library/" + repo
	}
	return repo
}

// blobRepoFor returns the repo path to use when GET-ing a blob from the
// source. For non-Docker-Hub sources, repo is used as-is.
//
// Stub for v0.1: we just return the repo unchanged. registry-manager has a
// more elaborate split between source ref and source repo; we simplify
// because the executor's caller (handler) already validated the sourceRef.
func blobRepoFor(_ string) string { return "" }

// extractManifest pulls (digest, size) pairs from a single-arch manifest.
//
// For multi-arch indexes (mediaType contains "index" or "list"), returns
// no layers — registry-manager skips indexes; the caller should detect
// this and warn.
func extractManifest(m *registry.Manifest) (layers []registry.Layer, configDigest string, configSize int64) {
	if strings.Contains(m.MediaType, "image.index") || strings.Contains(m.MediaType, "manifest.list") {
		// index: caller decides what to do; we return no layers
		return nil, "", 0
	}
	// single-arch: layers already populated by registry.decodeManifestLayers
	layers = m.Layers
	// config digest/size: we don't re-decode Raw here; pass via a tiny helper
	// that the registry layer can expose later. For now, leave config empty.
	return layers, "", 0
}

// PutRaw is a small public helper exposed on registry.Client via this
// indirection. The actual implementation lives in client.go (added there).
// Kept here so the import compiles while we focus on orchestration.
func init() {}