package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Chenjinteng/cairn/internal/credentials"
	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// ClientFactory builds a registry.Client for a sync source. The factory
// pattern lets the engine stay decoupled from proxy / TLS / credential
// resolution: the caller wires in whatever ClientConfig they have. The
// pull module wires this through pull.DefaultSourceResolver.
//
// username/password may be empty (anonymous pull).
type ClientFactory func(ctx context.Context, sourceURL, username, password string) (*registry.Client, error)

// CredentialLookup resolves a credential vault id to the source-registry
// auth pair. Returns ("", "", nil) when id is empty — the engine treats
// this as anonymous. Errors when id is non-empty but the lookup fails
// (vault closed / id removed) — those should fail the run loudly
// rather than silently downgrade.
type CredentialLookup func(ctx context.Context, credentialID string) (username, password string, err error)

// Engine runs sync tasks. One per process; Run is single-threaded per
// task (the REST handler is the concurrency boundary).
//
// Phase 1 implements pull only. The Push path lives in Phase 2; the
// engine is structured so the Push path will look like pull with src
// and dst swapped, sharing the same transfer primitives.
type Engine struct {
	// Local is the destination registry storage. For pull-direction
	// tasks this is the local cairn-managed registry.
	Local storage.Storage
	// NewClient builds source-side clients. Inject pull.DefaultSourceResolver
	// in production; tests can substitute a stub.
	NewClient ClientFactory
	// ResolveCredential is optional. When nil, all sync tasks run anonymous.
	// When non-nil, missing ids fail the run.
	ResolveCredential CredentialLookup
	// Logger receives per-image progress. nil → discard.
	Logger *slog.Logger
}

// NewEngine is the recommended constructor. local and newClient are
// required; resolveCredential and logger are optional.
func NewEngine(local storage.Storage, newClient ClientFactory, resolveCredential CredentialLookup, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{
		Local:             local,
		NewClient:         newClient,
		ResolveCredential: resolveCredential,
		Logger:            logger,
	}
}

// Run executes one task end-to-end. It returns the completed Run
// record and a non-nil error only on fatal failures (could not even
// reach the source, bad credentials, etc.); per-repo errors are
// captured in the run's Error field and the run still completes as
// "failed" — the operator can still see the partial counts.
//
// Lifecycle:
//  1. Resolve source client (auth via credential_id, or anonymous).
//  2. List source repositories (paginated _catalog under the hood).
//  3. Apply prefix + deny filters.
//  4. For each remaining repo, list tags and copy each (repo, tag).
//  5. Roll up counts, mark the run terminal in the store.
//
// The Run returned always carries a state: success / failed / skipped.
// Skipped is reserved for Phase 3's "previous run still in flight,
// cron tick skipped this run" — Phase 1 never returns skipped.
func (e *Engine) Run(ctx context.Context, t Task) (Run, error) {
	if t.ID == "" {
		return Run{}, errors.New("sync: task id required")
	}
	if t.Direction != DirectionPull {
		return Run{}, fmt.Errorf("sync: direction %q not implemented in Phase 1", t.Direction)
	}
	if t.SourceURL == "" {
		return Run{}, errors.New("sync: source_url required")
	}
	if e.Local == nil {
		return Run{}, errors.New("sync: engine missing local storage")
	}
	if e.NewClient == nil {
		return Run{}, errors.New("sync: engine missing client factory")
	}

	run := Run{
		ID:        newRunID(),
		TaskID:    t.ID,
		State:     RunStateSuccess,
		StartedAt: time.Now().UTC(),
	}

	username, password, err := e.lookupCredential(ctx, t.CredentialID)
	if err != nil {
		run.State = RunStateFailed
		run.Error = err.Error()
		run.FinishedAt = time.Now().UTC()
		return run, err
	}

	src, err := e.NewClient(ctx, t.SourceURL, username, password)
	if err != nil {
		run.State = RunStateFailed
		run.Error = fmt.Sprintf("build source client: %v", err)
		run.FinishedAt = time.Now().UTC()
		return run, err
	}

	counts, runErr := e.runPull(ctx, src, t, &run)
	run.ManifestsCopied = counts.ManifestsCopied
	run.BlobsCopied = counts.BlobsCopied
	run.BlobsSkipped = counts.BlobsSkipped
	run.BytesTotal = counts.BytesTotal
	run.FinishedAt = time.Now().UTC()
	if runErr != nil {
		run.State = RunStateFailed
		run.Error = runErr.Error()
		return run, runErr
	}
	return run, nil
}

// runPull walks the source catalog and copies each repo×tag into local
// storage. A per-image failure is logged and recorded into the run's
// Error string, but does NOT abort the run — the rest of the catalog
// is still attempted. A run that finishes with any per-image failure
// is marked failed overall.
func (e *Engine) runPull(ctx context.Context, src *registry.Client, t Task, run *Run) (Counts, error) {
	repos, err := src.ListRepositories(ctx)
	if err != nil {
		return Counts{}, fmt.Errorf("list source repos: %w", err)
	}
	if len(repos) == 0 {
		// Not an error — empty registries happen. Return zero counts.
		return Counts{}, nil
	}

	deny := NewDenyFilter(t.DenyList)
	prefix := NewPrefixFilter(t.SourceRepoPrefix)

	var counts Counts
	var firstErr error
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		if !prefix.Allows(repo) || !deny.Allows(repo) {
			continue
		}
		tags, terr := src.ListTags(ctx, repo)
		if terr != nil {
			e.Logger.Warn("sync: list tags failed", "repo", repo, "err", terr)
			if firstErr == nil {
				firstErr = fmt.Errorf("list tags %s: %w", repo, terr)
			}
			continue
		}
		for _, tag := range tags {
			if err := ctx.Err(); err != nil {
				return counts, err
			}
			mc, bc, bs, bt, ierr := e.copyOneImage(ctx, src, repo, tag, t.TargetRepoPrefix)
			counts.ManifestsCopied += mc
			counts.BlobsCopied += bc
			counts.BlobsSkipped += bs
			counts.BytesTotal += bt
			if ierr != nil {
				e.Logger.Warn("sync: copy failed", "repo", repo, "tag", tag, "err", ierr)
				if firstErr == nil {
					firstErr = fmt.Errorf("copy %s:%s: %w", repo, tag, ierr)
				}
			}
		}
	}
	if firstErr != nil {
		// Surface the first error to the caller; counts are still useful.
		return counts, firstErr
	}
	return counts, nil
}

// copyOneImage copies one (repo, tag) pair from src into local storage
// under the configured target repo prefix (M1: empty prefix → 1:1
// repo name; M2: prefix → target = prefix + sourceRepo).
//
// Returns (manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, err).
//
// Plan:
//
//  1. src.GetManifest(repo, tag)
//  2. Walk the manifest into a transferPlan (config + layers, recursing
//     into image indices)
//  3. For each blob: BlobExists → skip, otherwise stream + PATCH + PUT
//  4. Put each child manifest by digest (for indices)
//  5. Put the requested manifest by tag (so docker pull sees it)
func (e *Engine) copyOneImage(ctx context.Context, src *registry.Client, srcRepo, tag, targetPrefix string) (int64, int64, int64, int64, error) {
	destRepo := targetPrefix + srcRepo

	root, err := src.GetManifest(ctx, srcRepo, tag)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("get manifest: %w", err)
	}
	if root == nil {
		return 0, 0, 0, 0, fmt.Errorf("source returned nil manifest")
	}

	plan, err := planTransferForSync(ctx, src, srcRepo, root)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("plan transfer: %w", err)
	}

	var (
		manifestsCopied int64
		blobsCopied     int64
		blobsSkipped    int64
		bytesTotal      int64
	)
	for _, b := range plan.Blobs {
		if err := ctx.Err(); err != nil {
			return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, err
		}
		written, skipped, berr := e.transferBlob(ctx, src, srcRepo, destRepo, b.Digest)
		if berr != nil {
			return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, fmt.Errorf("transfer blob %s: %w", b.Digest, berr)
		}
		bytesTotal += written
		if skipped {
			blobsSkipped++
			continue
		}
		blobsCopied++
	}

	// Child manifests: write by digest so multi-arch lookups work.
	for _, c := range plan.Children {
		if err := ctx.Err(); err != nil {
			return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, err
		}
		if _, err := e.Local.PutManifest(ctx, destRepo, c.Ref, c.MediaType, c.Raw); err != nil {
			return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, fmt.Errorf("write child manifest %s: %w", c.Ref, err)
		}
		manifestsCopied++
	}

	// Finally, write the requested manifest by tag. This is what makes
	// `docker pull <destRepo>:<tag>` resolve; the digest-addressed
	// children above let the index resolve correctly.
	if _, err := e.Local.PutManifest(ctx, destRepo, tag, plan.RootMediaType, root.Raw); err != nil {
		return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, fmt.Errorf("write tag manifest %s:%s: %w", destRepo, tag, err)
	}
	manifestsCopied++

	return manifestsCopied, blobsCopied, blobsSkipped, bytesTotal, nil
}

// transferBlob streams one blob from the source registry into local
// storage. Skips blobs the destination already has.
//
// Mirrors pull.Orchestrator.transferBlob but lives here so the engine
// doesn't depend on the pull package. Phase 2 will need this same
// primitive for the push path (with dest = a registry.Client that
// speaks UploadBlob).
func (e *Engine) transferBlob(ctx context.Context, src *registry.Client, srcRepo, destRepo, digest string) (int64, bool, error) {
	exists, err := e.Local.BlobExists(ctx, destRepo, digest)
	if err != nil {
		return 0, false, err
	}
	if exists {
		return 0, true, nil
	}

	body, _, err := src.GetBlob(ctx, srcRepo, digest)
	if err != nil {
		return 0, false, err
	}
	defer body.Close()

	uuid, err := e.Local.StartUpload(ctx, destRepo)
	if err != nil {
		return 0, false, fmt.Errorf("start upload: %w", err)
	}
	written, err := e.Local.PatchUpload(ctx, destRepo, uuid, -1, body)
	if err != nil {
		_ = e.Local.CancelUpload(ctx, destRepo, uuid)
		return written, false, err
	}
	if err := e.Local.PutUpload(ctx, destRepo, uuid, digest); err != nil {
		_ = e.Local.CancelUpload(ctx, destRepo, uuid)
		return written, false, err
	}
	return written, false, nil
}

// lookupCredential resolves the task's credential_id. Returns
// ("", "", nil) when id is empty AND no resolver is configured.
// When a resolver exists but id is unknown, it returns an error so
// the run fails loudly — silently downgrading to anonymous would
// let the operator think private images are syncing.
func (e *Engine) lookupCredential(ctx context.Context, credentialID string) (string, string, error) {
	if credentialID == "" {
		return "", "", nil
	}
	if e.ResolveCredential == nil {
		return "", "", fmt.Errorf("sync: task references credential %q but no vault configured", credentialID)
	}
	return e.ResolveCredential(ctx, credentialID)
}

// newRunID returns a 16-char hex string. The id is only used for
// log correlation and DB primary key; uniqueness across the world
// is provided by the random suffix, not by anything semantic.
func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sr-" + hex.EncodeToString(b[:])
}

// --- planning primitives (kept here so engine.go is self-contained
// for Phase 1; Phase 2 may move them to a shared package with pull).

// plannedBlobRef is one blob the engine needs to fetch.
type plannedBlobRef struct {
	Digest string
	Size   int64
}

// plannedManifest is one child manifest inside a multi-arch image.
type plannedManifest struct {
	Ref       string
	MediaType string
	Raw       []byte
}

// transferPlan is the work list for one (repo, tag) copy.
type transferPlan struct {
	RootMediaType string
	Children      []plannedManifest
	Blobs         []plannedBlobRef
}

// sourceDescDesc and sourceDesc mirror pull.executor.go's private types
// but live here so engine.go does not depend on the pull package.
// Kept minimal: just the fields the decoder walks.
type sourceDescDesc struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant,omitempty"`
	} `json:"platform,omitempty"`
}

type sourceManifestDoc struct {
	MediaType string          `json:"mediaType"`
	Config    *sourceDescDesc `json:"config"`
	Layers    []sourceDescDesc `json:"layers"`
	Manifests []sourceDescDesc `json:"manifests"`
}

// manifestFetcher matches both *registry.Client and any test stub.
// Same shape as pull.manifestFetcher but defined here to keep this
// package self-contained.
type manifestFetcher interface {
	GetManifest(ctx context.Context, repo, reference string) (*registry.Manifest, error)
}

// planTransferForSync expands a source manifest into the work list.
//
// Phase 1 scope:
//
//   - Index manifests: descend into all children (no platform filter
//     yet — sync is "copy the whole thing"; platform allow-list is a
//     pull-page concept, not a sync concept).
//   - Single-arch manifests: just config + layers.
//
// Phase 2+ may add platform filtering, but per ROADMAP "按目标前缀" is
// the only repo-level mapping for now.
func planTransferForSync(ctx context.Context, src manifestFetcher, srcRepo string, root *registry.Manifest) (*transferPlan, error) {
	if root == nil {
		return nil, fmt.Errorf("source manifest is nil")
	}
	doc, err := decodeSourceDoc(root.Raw)
	if err != nil {
		return nil, err
	}
	plan := &transferPlan{}
	seen := map[string]bool{}

	if len(doc.Manifests) > 0 {
		plan.RootMediaType = firstNonEmpty(root.MediaType, doc.MediaType, "application/vnd.oci.image.index.v1+json")
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
			plan.Children = append(plan.Children, plannedManifest{
				Ref:       child.Digest,
				MediaType: firstNonEmpty(m.MediaType, child.MediaType, childDoc.MediaType, "application/vnd.oci.image.manifest.v1+json"),
				Raw:       m.Raw,
			})
			collectBlobs(&plan.Blobs, seen, childDoc)
		}
		return plan, nil
	}

	plan.RootMediaType = firstNonEmpty(root.MediaType, doc.MediaType, "application/vnd.oci.image.manifest.v1+json")
	collectBlobs(&plan.Blobs, seen, doc)
	return plan, nil
}

func collectBlobs(out *[]plannedBlobRef, seen map[string]bool, doc sourceManifestDoc) {
	add := func(d sourceDescDesc) {
		if d.Digest == "" || seen[d.Digest] {
			return
		}
		seen[d.Digest] = true
		*out = append(*out, plannedBlobRef{Digest: d.Digest, Size: d.Size})
	}
	if doc.Config != nil {
		add(*doc.Config)
	}
	for _, l := range doc.Layers {
		add(l)
	}
}

func decodeSourceDoc(raw []byte) (sourceManifestDoc, error) {
	var doc sourceManifestDoc
	if len(raw) == 0 {
		return doc, fmt.Errorf("manifest body is empty")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return sourceManifestDoc{}, fmt.Errorf("decode manifest: %w", err)
	}
	return doc, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- end planning primitives

// --- public helpers exposed for the credentials module wiring ---

// VaultLookup adapts an internal/credentials.Vault into the engine's
// CredentialLookup signature. Returns an error if the vault is nil.
// The returned pair is ("", "") if the credential is missing — callers
// should treat that as anonymous, not an error. Wait: actually the
// vault distinguishes "id known, password empty" (anonymous-capable
// user) from "id unknown" (deleted). The engine treats "id unknown"
// as an error so a stale task fails loudly instead of silently
// downgrading.
func VaultLookup(v *credentials.Vault) CredentialLookup {
	return func(ctx context.Context, credentialID string) (string, string, error) {
		if v == nil {
			return "", "", errors.New("sync: credential vault not initialised")
		}
		c, err := v.Get(credentialID)
		if err != nil {
			return "", "", err
		}
		if c.ID == "" {
			return "", "", fmt.Errorf("sync: credential %q not found", credentialID)
		}
		return c.Username, c.Password, nil
	}
}