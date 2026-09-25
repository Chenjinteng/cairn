package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cairn/internal/config"
	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/proxies"
	"cairn/internal/registry"
	"cairn/internal/storage"
)

// DefaultUpstream is the upstream used when the job, its credential and the
// operator-configured default all come up empty. Docker Hub is the least
// surprising fallback for `SourceRef: "nginx:1.27"`.
//
// Exported so the API layer can pre-resolve the same value the executor will
// actually use, and show it in the UI before the job starts running.
const DefaultUpstream = "https://registry-1.docker.io"

// Media types we may have to synthesise when the source omits one.
const (
	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex    = "application/vnd.oci.image.index.v1+json"
)

// dockerHubHosts are the hosts that use Docker Hub's implicit "library/"
// namespace for official images. Only these get the prefix: on a private
// registry, a repo called "nginx" means literally "nginx", not "library/nginx".
var dockerHubHosts = map[string]bool{
	"registry-1.docker.io":    true,
	"docker.io":               true,
	"index.docker.io":         true,
	"registry.hub.docker.com": true,
}

// SourceResolver returns a Registry client configured to talk to the upstream
// registry described by sourceURL, optionally using inline credentials + proxy.
//
// Used by the executor: we want one Client per pull source so the Bearer token
// cache is scoped to that source (not cross-contaminated).
type SourceResolver func(sourceURL, username, password, proxyURL string) (*registry.Client, error)

// DefaultSourceResolver wires credentials + proxy into a registry.Client.
// Use this from server.Build; tests can swap it.
//
// The 15m timeout bounds a single upstream HTTP round trip, not the whole job.
// A cold multi-GB layer over a slow link legitimately takes minutes, and the
// default 30s would abort it mid-stream.
func DefaultSourceResolver() SourceResolver {
	return func(sourceURL, username, password, proxyURL string) (*registry.Client, error) {
		base, err := registry.NormalizeBaseURL(sourceURL)
		if err != nil {
			return nil, err
		}
		return registry.NewClient(registry.Config{
			BaseURL:  base,
			Username: username,
			Password: password,
			Proxy:    proxyURL,
			Timeout:  15 * time.Minute,
		})
	}
}

// Orchestrator is the actual pull implementation.
//
// v0.5: the destination is ALWAYS the local storage — cairn is the registry,
// so there is nothing to configure on the destination side. Reads come from an
// upstream named per job, falling back to the credential's URL, then to
// DefaultSourceURL (REGISTRY_URL), then to Docker Hub. There is no "managed
// registry" field any more; REGISTRY_URL survives only as a default upstream.
type Orchestrator struct {
	Dest       storage.Storage // destination: always the local registry
	SrcResolve SourceResolver
	Vault      *credentials.Vault
	Proxies    *proxies.Store
	DB         *db.Db

	// Mutable (v0.5.1) is the live, runtime-editable settings struct. The
	// pull source chain reads it through Mutable.RegistryURL() so changes
	// via PATCH /api/config take effect on the next queued job without a
	// restart. nil is OK -- resolveSource treats it as "env default only".
	Mutable *config.Mutable
	// DefaultSourceURL is the env bootstrap upstream (deprecated v0.5.1:
	// superseded by Mutable; kept so resolveSource can fall back to it
	// when Mutable is nil OR was never written to).
	DefaultSourceURL string

	PullHistoryRetention int
}

// RunOne executes one job to completion; returns nil on success.
//
// Lifecycle:
//  1. split sourceRef into <repo>:<tag> (tag defaults to "latest")
//  2. resolve upstream URL + auth + proxy (see resolveSource)
//  3. fetch the source manifest
//  4. plan the transfer: an index is expanded into its children, and every
//     referenced blob (config + layers) is deduplicated by digest
//  5. stream each blob from source to local storage (skipping ones we have)
//  6. write child manifests by digest, then the requested manifest by tag
//  7. record the terminal state to SQLite (history)
func (o *Orchestrator) RunOne(ctx context.Context, j *Job) error {
	v := j.View()

	srcRepo, srcTag := splitRef(v.SourceRef)
	if srcRepo == "" {
		return fmt.Errorf("source ref %q has no repository", v.SourceRef)
	}
	if srcTag == "" {
		// `docker pull nginx` means nginx:latest; be equally forgiving.
		srcTag = "latest"
	}

	destRepo := strings.TrimSpace(v.DestRepo)
	destTag := strings.TrimSpace(v.DestTag)
	if destRepo == "" {
		destRepo = srcRepo
	}
	if destTag == "" {
		destTag = srcTag
	}

	srcURL, user, pass, proxyURL, err := o.resolveSource(v)
	if err != nil {
		return err
	}
	srcRepo = QualifySourceRepo(srcURL, srcRepo)

	// Publish the effective upstream: a job may name none of its own, and the
	// UI shows "image came from X" for queued and running jobs alike.
	j.MutexHeld(func(vv *JobView) { vv.SourceURL = srcURL })

	src, err := o.SrcResolve(srcURL, user, pass, proxyURL)
	if err != nil {
		return fmt.Errorf("source resolver: %w", err)
	}

	srcManifest, err := src.GetManifest(ctx, srcRepo, srcTag)
	if err != nil {
		return fmt.Errorf("fetch source manifest %s:%s from %s: %w", srcRepo, srcTag, srcURL, err)
	}

	plan, err := planTransfer(ctx, src, srcRepo, srcManifest)
	if err != nil {
		return err
	}

	j.MutexHeld(func(vv *JobView) {
		vv.BlobsTotal = len(plan.blobs)
		vv.BytesTotal = 0
		for _, b := range plan.blobs {
			vv.BytesTotal += b.Size
		}
	})

	// 1. Push every blob the root (and any children) reference. Deduplicated
	//    by digest, so a multi-arch index whose platforms share a base layer
	//    transfers that layer exactly once.
	for i, b := range plan.blobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.transferBlob(ctx, src, srcRepo, destRepo, b.Digest, b.Size); err != nil {
			return fmt.Errorf("blob %d/%d (%s): %w", i+1, len(plan.blobs), b.Digest, err)
		}
		j.MutexHeld(func(vv *JobView) {
			vv.BlobsDone++
			vv.BytesDone += b.Size
		})
	}

	// 2. Child manifests must exist before the index that points at them,
	//    or a concurrent pull would 404 on the child digest.
	for _, child := range plan.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := o.Dest.PutManifest(ctx, destRepo, child.Ref, child.MediaType, child.Raw); err != nil {
			return fmt.Errorf("write child manifest %s: %w", child.Ref, err)
		}
	}

	// 3. Write the manifest the user asked for, under the destination tag.
	written, err := o.Dest.PutManifest(ctx, destRepo, destTag, plan.rootMediaType, srcManifest.Raw)
	if err != nil {
		return fmt.Errorf("write manifest %s:%s: %w", destRepo, destTag, err)
	}
	if written == "" {
		written = srcManifest.Digest
	}
	j.MutexHeld(func(vv *JobView) { vv.FinalDigest = written })

	if o.DB != nil {
		vv := j.View()
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
	return nil
}

// resolveSource determines the upstream URL, credentials and proxy for one job.
//
// Precedence — v0.5 removed the "managed registry" concept, so nothing here is
// required configuration; every input is an optional override:
//
//	URL:   job.sourceUrl  >  credential.URL  >  DefaultSourceURL  >  Docker Hub
//	auth:  inline username/password  >  the credential's username/password
//	proxy: job.sourceProxy (inline URL)  >  job.sourceProxyId (stored proxy)
func (o *Orchestrator) resolveSource(v JobView) (srcURL, user, pass, proxyURL string, err error) {
	var cred *credentials.Credential
	if v.Credential != "" {
		if o.Vault == nil {
			return "", "", "", "", fmt.Errorf("credential %q: credential store unavailable", v.Credential)
		}
		c, gerr := o.Vault.Get(v.Credential)
		if gerr != nil {
			return "", "", "", "", fmt.Errorf("credential %q: %w", v.Credential, gerr)
		}
		cred = &c
	}

	srcURL = strings.TrimSpace(v.SourceURL)
	user = strings.TrimSpace(v.SourceUser)
	pass = v.SourcePass
	if cred != nil {
		if srcURL == "" {
			srcURL = strings.TrimSpace(cred.URL)
		}
		if user == "" {
			user = cred.Username
		}
		if pass == "" {
			pass = cred.Password
		}
	}
	if srcURL == "" {
		if o.Mutable != nil {
			srcURL = o.Mutable.RegistryURL()
		}
	}
	if srcURL == "" {
		if o.Mutable != nil {
			srcURL = o.Mutable.RegistryURL()
		} else {
			srcURL = strings.TrimSpace(o.DefaultSourceURL)
		}
	}
	if srcURL == "" {
		srcURL = DefaultUpstream
	}

	proxyURL = strings.TrimSpace(v.ProxyURL)
	if v.Proxy != "" {
		if o.Proxies == nil {
			return "", "", "", "", fmt.Errorf("proxy %q: proxy store unavailable", v.Proxy)
		}
		p, gerr := o.Proxies.Get(v.Proxy)
		if gerr != nil {
			return "", "", "", "", fmt.Errorf("proxy %q: %w", v.Proxy, gerr)
		}
		if proxyURL == "" {
			proxyURL = p.URL
		}
	}
	return srcURL, user, pass, proxyURL, nil
}

// hostOfRegistryURL extracts the bare host (no scheme, no path, no port kept
// when it is the default 443… we keep the port, it is part of the host key).
func hostOfRegistryURL(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// QualifySourceRepo applies Docker Hub's implicit "library/" namespace.
// "nginx" on Docker Hub is really "library/nginx"; the same ref against a
// private registry is a repo literally called "nginx".
func QualifySourceRepo(sourceURL, repo string) string {
	if strings.Contains(repo, "/") {
		return repo
	}
	if !dockerHubHosts[hostOfRegistryURL(sourceURL)] {
		return repo
	}
	return "library/" + repo
}

// blobRef is one content-addressed blob to copy, with the size the source
// advertised (used for progress reporting; the real size comes from the wire).
type blobRef struct {
	Digest string
	Size   int64
}

// plannedManifest is a child manifest of a multi-arch index, ready to store.
type plannedManifest struct {
	// Ref is the digest the parent index references. We store under Ref (not
	// the digest we computed from the body) so that a source that served a
	// mismatched body fails loudly in storage instead of silently producing an
	// index whose children are unreachable.
	Ref       string
	MediaType string
	Raw       []byte
}

// transferPlan is the full work list for one pull.
type transferPlan struct {
	rootMediaType string
	children      []plannedManifest
	blobs         []blobRef
}

// sourceManifestDoc covers both a single-arch manifest (config + layers) and a
// multi-arch index (manifests).
//
// We decode the raw JSON rather than trusting registry.Manifest's normalized
// fields because GetManifest deliberately does not descend into an index: for
// an index it leaves Layers empty and reports the first child's platform. The
// raw bytes are the source of truth.
type sourceManifestDoc struct {
	MediaType string       `json:"mediaType"`
	Config    *sourceDesc  `json:"config"`
	Layers    []sourceDesc `json:"layers"`
	Manifests []sourceDesc `json:"manifests"`
}

type sourceDesc struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func decodeSourceDoc(raw []byte) (sourceManifestDoc, error) {
	var doc sourceManifestDoc
	if len(raw) == 0 {
		return doc, fmt.Errorf("manifest body is empty")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("decode manifest: %w", err)
	}
	return doc, nil
}

// planTransfer expands the root manifest into the exact set of blobs and child
// manifests to copy. For a multi-arch index every child is fetched up front:
// they are small JSON documents, and having them lets us report an accurate
// blob total before the first byte moves.
func planTransfer(ctx context.Context, src *registry.Client, srcRepo string, root *registry.Manifest) (*transferPlan, error) {
	if root == nil {
		return nil, fmt.Errorf("source manifest is nil")
	}
	doc, err := decodeSourceDoc(root.Raw)
	if err != nil {
		return nil, err
	}

	plan := &transferPlan{}
	seen := make(map[string]bool)

	if len(doc.Manifests) > 0 {
		plan.rootMediaType = firstNonEmpty(root.MediaType, doc.MediaType, mediaTypeOCIIndex)
		for _, child := range doc.Manifests {
			if child.Digest == "" {
				continue
			}
			m, err := src.GetManifest(ctx, srcRepo, child.Digest)
			if err != nil {
				return nil, fmt.Errorf("fetch child manifest %s: %w", child.Digest, err)
			}
			childDoc, err := decodeSourceDoc(m.Raw)
			if err != nil {
				return nil, fmt.Errorf("child manifest %s: %w", child.Digest, err)
			}
			plan.children = append(plan.children, plannedManifest{
				Ref:       child.Digest,
				MediaType: firstNonEmpty(m.MediaType, child.MediaType, childDoc.MediaType, mediaTypeOCIManifest),
				Raw:       m.Raw,
			})
			collectBlobs(&plan.blobs, seen, childDoc)
		}
		return plan, nil
	}

	plan.rootMediaType = firstNonEmpty(root.MediaType, doc.MediaType, mediaTypeOCIManifest)
	collectBlobs(&plan.blobs, seen, doc)
	return plan, nil
}

// collectBlobs appends the config + layer digests of one manifest document,
// skipping digests already scheduled.
func collectBlobs(out *[]blobRef, seen map[string]bool, doc sourceManifestDoc) {
	add := func(d sourceDesc) {
		if d.Digest == "" || seen[d.Digest] {
			return
		}
		seen[d.Digest] = true
		*out = append(*out, blobRef{Digest: d.Digest, Size: d.Size})
	}
	if doc.Config != nil {
		add(*doc.Config)
	}
	for _, l := range doc.Layers {
		add(l)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// transferBlob streams one blob from the source registry into local storage.
//
// Skip-if-present optimisation: a cheap existence check first, because pulling
// a second tag of an image usually shares every layer with the first.
//
// We do not reuse the multi-step upload API: we start an upload, PATCH the
// whole body once, then PUT-commit with the digest — the same three calls the
// /v2/* routes make, so the resulting on-disk state is identical.
func (o *Orchestrator) transferBlob(ctx context.Context, src *registry.Client, srcRepo, destRepo, digest string, size int64) error {
	exists, err := o.Dest.BlobExists(ctx, destRepo, digest)
	if err != nil && !errors.Is(err, storage.ErrInvalidDigest) {
		return err
	}
	if exists {
		return nil
	}

	// srcRepo matters: a blob can live in several repos upstream, and registries
	// (Docker Hub included) 404 the cross-repo GET.
	body, _, err := src.GetBlob(ctx, srcRepo, digest)
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
//
// The tag separator is the LAST colon that isn't followed by a slash, so a
// registry-with-port prefix ("proxy.example.com:10001/nginx:1.27") splits correctly.
func splitRef(ref string) (repo, tag string) {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		if !strings.Contains(ref[i+1:], "/") {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}
