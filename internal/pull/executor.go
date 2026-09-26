package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Cfg (v0.5.4) is the live Config pointer; resolveSource reads the
	// global HTTP proxy from it via RegistryProxy(). nil is OK
	// -- proxy then falls back to per-job overrides only.
	Cfg *config.Config
	// DefaultSourceURL is the env bootstrap upstream (deprecated v0.5.1:
	// superseded by Mutable; kept so resolveSource can fall back to it
	// when Mutable is nil OR was never written to).
	DefaultSourceURL string

	PullHistoryRetention int
}

// platformAllow returns the multi-arch allow-list for the next transfer.
//
// It deliberately reads the live config instead of caching the value in a
// struct field: an unset field is indistinguishable from "pull every
// platform" (which is the documented meaning of an empty list), so a
// forgotten assignment at the construction site silently changes behaviour.
// v0.5.15 shipped exactly that — a nil func field, called unguarded, took
// the whole process down. Reading Cfg.PullPlatforms() is nil-safe on both
// the receiver and the config. (v0.5.16)
func (o *Orchestrator) platformAllow() []string {
	if o == nil {
		return nil
	}
	return o.Cfg.PullPlatforms()
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

	updatePhase(j, 0, func(p *Phase) {
		p.Status = PhaseRunning
		p.Message = "Fetching source manifest..."
	})
	srcManifest, err := src.GetManifest(ctx, srcRepo, srcTag)
	if err != nil {
		updatePhase(j, 0, func(p *Phase) {
			p.Status = PhaseFailed
			p.Message = "Failed to fetch source manifest"
		})
		return fmt.Errorf("fetch source manifest %s:%s from %s: %w", srcRepo, srcTag, srcURL, err)
	}

	plan, err := planTransfer(ctx, src, srcRepo, srcManifest, o.platformAllow())
	if err != nil {
		updatePhase(j, 0, func(p *Phase) {
			p.Status = PhaseFailed
			p.Message = "Failed to expand manifest"
		})
		return err
	}

	// Classify the root: an index has children (each with its own config,
	// so no single config blob to label); a single-arch manifest has
	// exactly one config blob we can call out like registry-manager does.
	doc, docErr := decodeSourceDoc(srcManifest.Raw)
	isIndex := docErr == nil && len(doc.Manifests) > 0
	configDigest := ""
	if docErr == nil && doc.Config != nil {
		configDigest = doc.Config.Digest
	}
	manifestMsg := "Manifest downloaded"
	if isIndex {
		manifestMsg = fmt.Sprintf("Manifest index downloaded (%d platforms)", len(plan.children))
	}
	updatePhase(j, 0, func(p *Phase) {
		p.Status = PhaseSuccess
		p.Digest = srcManifest.Digest
		p.Message = manifestMsg
	})

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
		name := fmt.Sprintf("blob:%d", i)
		if !isIndex && b.Digest == configDigest {
			name = "config"
		}
		sz := b.Size
		phaseIdx := appendPhase(j, Phase{
			Name:       name,
			Digest:     b.Digest,
			Status:     PhaseRunning,
			TotalBytes: &sz,
		})
		n, skipped, terr := o.transferBlob(ctx, src, srcRepo, destRepo, b.Digest,
			func(done int64) {
				updatePhase(j, phaseIdx, func(p *Phase) { p.Bytes = done })
			})
		j.MutexHeld(func(vv *JobView) {
			vv.BlobsDone++
			if skipped {
				// Counted toward the total even though nothing moved:
				// the progress bar must still reach 100%.
				vv.BytesDone += b.Size
			} else {
				vv.BytesDone += n
			}
		})
		if terr != nil {
			updatePhase(j, phaseIdx, func(p *Phase) {
				p.Status = PhaseFailed
				p.Bytes = n
				p.Message = "Transfer failed"
			})
			return fmt.Errorf("blob %d/%d (%s): %w", i+1, len(plan.blobs), b.Digest, terr)
		}
		if skipped {
			updatePhase(j, phaseIdx, func(p *Phase) {
				p.Status = PhaseSkipped
				p.Message = "Already exists, skipped"
			})
		} else {
			updatePhase(j, phaseIdx, func(p *Phase) {
				p.Status = PhaseSuccess
				p.Bytes = n
			})
		}
	}

	// 2. Child manifests must exist before the index that points at them,
	//    or a concurrent pull would 404 on the child digest.
	if len(plan.children) > 0 {
		childIdx := appendPhase(j, Phase{
			Name:    "child-manifests",
			Status:  PhaseRunning,
			Message: fmt.Sprintf("Writing %d child manifests...", len(plan.children)),
		})
		for ci, child := range plan.children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := o.Dest.PutManifest(ctx, destRepo, child.Ref, child.MediaType, child.Raw); err != nil {
				updatePhase(j, childIdx, func(p *Phase) {
					p.Status = PhaseFailed
					p.Message = fmt.Sprintf("Child manifest %d/%d write failed", ci+1, len(plan.children))
				})
				return fmt.Errorf("write child manifest %s: %w", child.Ref, err)
			}
		}
		updatePhase(j, childIdx, func(p *Phase) {
			p.Status = PhaseSuccess
			p.Message = fmt.Sprintf("%d child manifests written", len(plan.children))
		})
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
	// v0.5.9: the global registry.proxy setting was removed. Per-job
	// proxy (cfg.Proxy on the credential) still works; the panel no
	// longer exposes a single global proxy because cairn itself doesn't
	// need one — outbound goes direct, and operators reach external
	// registries through their own VPN/SSH/SOCKS tunnel.
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
	MediaType string             `json:"mediaType"`
	Digest    string             `json:"digest"`
	Size      int64              `json:"size"`
	Platform  *sourcePlatformRef `json:"platform,omitempty"` // index entries only
}

// sourcePlatformRef is the {architecture,os[,variant]} object the registry
// attaches to each child in an image index's manifests[]. Empty Variant is
// normal (amd64, arm64, ppc64le, s390x, riscv64, ... all have none); arm/v7
// and arm/v6 carry "v7"/"v6".
type sourcePlatformRef struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

// platformKey returns "os/arch" or "os/arch/variant" — the canonical token
// shape MutableKeys "pull.platforms" accepts. Lowercased to match the
// allow-list parsing in config.PullPlatforms.
func (p *sourcePlatformRef) key() string {
	if p == nil {
		return ""
	}
	os := strings.ToLower(strings.TrimSpace(p.OS))
	arch := strings.ToLower(strings.TrimSpace(p.Architecture))
	if os == "" || arch == "" {
		return ""
	}
	if v := strings.ToLower(strings.TrimSpace(p.Variant)); v != "" {
		return os + "/" + arch + "/" + v
	}
	return os + "/" + arch
}

// matchAny reports whether this platform's key appears in the allow-list.
// An empty allow-list means "all platforms" (no filter).
func (p *sourcePlatformRef) matchAny(allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	k := p.key()
	if k == "" {
		// No platform info at all: keep it. Better to pull an "unknown"
		// child than to silently drop the only manifest the registry gave.
		return true
	}
	for _, a := range allow {
		if a == k {
			return true
		}
	}
	return false
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

// manifestFetcher is the slice of *registry.Client that planTransfer uses.
// Keeping it as a one-method interface lets unit tests inject a fake
// without HTTP mocking or depending on the concrete Client struct.
type manifestFetcher interface {
	GetManifest(ctx context.Context, repo, reference string) (*registry.Manifest, error)
}

// planTransfer expands the root manifest into the exact set of blobs and child
// manifests to copy. For a multi-arch index every child is fetched up front:
// they are small JSON documents, and having them lets us report an accurate
// blob total before the first byte moves.
//
// platformAllow filters multi-arch index children: empty = all (current
// behaviour); non-empty = only children whose {os,architecture[,variant]}
// appears in the list. If filtering leaves zero matches the pull fails
// clearly rather than silently producing an empty index.
func planTransfer(ctx context.Context, src manifestFetcher, srcRepo string, root *registry.Manifest, platformAllow []string) (*transferPlan, error) {
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
		matched := 0
		for _, child := range doc.Manifests {
			if child.Digest == "" {
				continue
			}
			if !child.Platform.matchAny(platformAllow) {
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
			matched++
		}
		if matched == 0 {
			return nil, fmt.Errorf("platform filter %v matched no child manifests in the source index", platformAllow)
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
//
// Returns the bytes written and whether the blob was skipped (the
// destination already had it). onProgress, when non-nil, receives the
// cumulative bytes read so far — the pull UI's per-blob progress line.
func (o *Orchestrator) transferBlob(ctx context.Context, src *registry.Client, srcRepo, destRepo, digest string, onProgress func(done int64)) (written int64, skipped bool, err error) {
	exists, err := o.Dest.BlobExists(ctx, destRepo, digest)
	if err != nil && !errors.Is(err, storage.ErrInvalidDigest) {
		return 0, false, err
	}
	if exists {
		return 0, true, nil
	}

	// srcRepo matters: a blob can live in several repos upstream, and registries
	// (Docker Hub included) 404 the cross-repo GET.
	body, _, err := src.GetBlob(ctx, srcRepo, digest)
	if err != nil {
		return 0, false, err
	}
	defer body.Close()

	var stream io.Reader = body
	if onProgress != nil {
		stream = &countingReader{r: body, onProgress: onProgress}
	}

	uuid, err := o.Dest.StartUpload(ctx, destRepo)
	if err != nil {
		return 0, false, err
	}
	written, err = o.Dest.PatchUpload(ctx, destRepo, uuid, -1, stream)
	if err != nil {
		_ = o.Dest.CancelUpload(ctx, destRepo, uuid)
		return written, false, err
	}
	if err := o.Dest.PutUpload(ctx, destRepo, uuid, digest); err != nil {
		_ = o.Dest.CancelUpload(ctx, destRepo, uuid)
		return written, false, err
	}
	return written, false, nil
}

// countingReader reports cumulative bytes read. The pull executor wraps the
// source blob body in one so the UI can show per-blob progress while the
// stream is still moving.
type countingReader struct {
	r          io.Reader
	n          int64
	onProgress func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		if c.onProgress != nil {
			c.onProgress(c.n)
		}
	}
	return n, err
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
