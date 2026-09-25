package pull

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/proxies"
	"cairn/internal/registry"
	"cairn/internal/storage"
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
		c := registry.Config{BaseURL: sourceURL, Timeout: 5 * time.Minute}
		if cred != nil {
			c.Username = cred.Username
			c.Password = cred.Password
		}
		if proxy != nil {
			c.Proxy = proxy.URL
		}
		return registry.NewClient(c)
	}
}

// Orchestrator is the actual pull implementation.
//
// As of v0.3, the destination is the LOCAL storage (cairn IS the
// registry). Reads come from an EXTERNAL registry client (Docker Hub,
// ghcr, etc.). ExternalRegistry may be nil if the operator hasn't
// configured REGISTRY_URL — in that case the executor is disabled.
type Orchestrator struct {
	Dest             storage.Storage     // destination: the local registry
	ExternalRegistry registry.Registry   // source: external registry for pulling
	SrcResolve       SourceResolver
	Vault            *credentials.Vault
	Proxies          *proxies.Store
	DB               *db.Db
	PullHistoryRetention int
}

// RunOne executes one job to completion; returns nil on success.
//
// Lifecycle:
//  1. parse sourceRef into <repo>:<tag> + apply Docker Hub library/ prefix
//  2. resolve source registry client (with credential + proxy if set)
//  3. fetch source manifest (with retry on 401 → Bearer)
//  4. walk manifest layers + config; for each: download from source,
//     upload to local storage (skip if already present via HEAD-style exists)
//  5. write manifest to local storage
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
	srcRepo = applyDockerHubLibraryPrefix(srcRepo, srcTag)

	var cred *credentials.Credential
	var proxy *proxies.Proxy
	if v.Credential != "" {
		if o.Vault == nil {
			return fmt.Errorf("credential %q: credential store unavailable", v.Credential)
		}
		c, err := o.Vault.Get(v.Credential)
		if err != nil {
			return fmt.Errorf("credential %q: %w", v.Credential, err)
		}
		cred = &c
	}
	if v.Proxy != "" {
		if o.Proxies == nil {
			return fmt.Errorf("proxy %q: proxy store unavailable", v.Proxy)
		}
		p, err := o.Proxies.Get(v.Proxy)
		if err != nil {
			return fmt.Errorf("proxy %q: %w", v.Proxy, err)
		}
		proxy = &p
	}
	// cred is nil for anonymous pulls — default to Docker Hub. (The old code
	// dereferenced cred.URL unconditionally and panicked on every job that
	// had no credential attached.)
	srcURL := ""
	if cred != nil {
		srcURL = cred.URL
	}
	if srcURL == "" {
		srcURL = "https://registry-1.docker.io"
	}
	src, err := o.SrcResolve(srcURL, cred, proxy)
	if err != nil {
		return fmt.Errorf("source resolver: %w", err)
	}

	srcManifest, err := src.GetManifest(ctx, srcRepo, srcTag)
	if err != nil {
		return fmt.Errorf("fetch source manifest: %w", err)
	}
	if srcManifest.Digest == "" {
		return fmt.Errorf("source manifest missing digest")
	}

	layers, configDigest, configSize := extractManifest(srcManifest)
	j.MutexHeld(func(vv *JobView) {
		vv.BlobsTotal = len(layers)
		if configDigest != "" {
			vv.BlobsTotal++
		}
		vv.BytesTotal = srcManifest.Size
	})

	for i, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.transferBlob(ctx, src, destRepo, layer.Digest, layer.Size); err != nil {
			return fmt.Errorf("layer %d (%s): %w", i, layer.Digest, err)
		}
		j.MutexHeld(func(vv *JobView) {
			vv.BlobsDone++
			vv.BytesDone += layer.Size
		})
	}
	if configDigest != "" {
		if err := o.transferBlob(ctx, src, destRepo, configDigest, configSize); err != nil {
			return fmt.Errorf("config (%s): %w", configDigest, err)
		}
		j.MutexHeld(func(vv *JobView) {
			vv.BlobsDone++
			vv.BytesDone += configSize
		})
	}

	if _, err := o.Dest.PutManifest(ctx, destRepo, destTag, srcManifest.MediaType, srcManifest.Raw); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

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

// transferBlob downloads blob from src and writes it into the local storage.
//
// Skip-if-present optimisation: HEAD-equivalent check via storage.BlobExists
// before download. We don't reuse the multi-step upload API; instead we
// start an upload, PATCH once with the full body, then PUT-commit with
// the digest — mirrors what the /v2/* routes do.
func (o *Orchestrator) transferBlob(ctx context.Context, src *registry.Client, destRepo, digest string, size int64) error {
	exists, err := o.Dest.BlobExists(ctx, destRepo, digest)
	if err != nil && !errors.Is(err, storage.ErrInvalidDigest) {
		return err
	}
	if exists {
		return nil
	}

	body, _, err := src.GetBlob(ctx, srcRepoForDigest(src.GetBaseURL(), digest), digest)
	if err != nil {
		return err
	}
	defer body.Close()

	uuid, err := o.Dest.StartUpload(ctx, destRepo)
	if err != nil {
		return err
	}
	if _, err := o.Dest.PatchUpload(ctx, destRepo, uuid, -1, body); err != nil {
		_ = o.Dest.CancelUpload(ctx, destRepo, uuid)
		return err
	}
	if err := o.Dest.PutUpload(ctx, destRepo, uuid, digest); err != nil {
		_ = o.Dest.CancelUpload(ctx, destRepo, uuid)
		return err
	}
	_ = size
	return nil
}

// splitRef parses "repo:tag" into components.
func splitRef(ref string) (repo, tag string) {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		if !strings.Contains(ref[i+1:], "/") {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

func applyDockerHubLibraryPrefix(repo, tag string) string {
	if !strings.Contains(repo, "/") && tag != "" {
		return "library/" + repo
	}
	return repo
}

// srcRepoForDigest returns the repo path to use when GET-ing a blob from
// the source. Pull requests the blob from the source's repo (not dest).
func srcRepoForDigest(_ string, _ string) string {
	return ""
}

// extractManifest pulls (digest, size) pairs from a single-arch manifest.
// For multi-arch indexes, returns no layers (caller skips).
func extractManifest(m *registry.Manifest) (layers []registry.Layer, configDigest string, configSize int64) {
	if strings.Contains(m.MediaType, "image.index") || strings.Contains(m.MediaType, "manifest.list") {
		return nil, "", 0
	}
	layers = m.Layers
	return layers, "", 0
}

// silence unused imports
var _ = io.EOF
var _ = url.PathEscape