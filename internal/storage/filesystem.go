package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Chenjinteng/cairn/internal/config"
)

// Filesystem is the production Storage impl, backed by a local directory.
//
// Concurrency model:
//   - Per-repo exclusive lock for write paths (manifest PUT, tag write).
//     Implemented via a sync.Map keyed by repo name; the lock value is a
//     channel (acquire by sending, release by receiving).
//   - Blobs are content-addressed, so concurrent writes for the SAME digest
//     are safe (we just verify and rename). Different digests under the
//     same repo can run in parallel.
//   - Read paths are lock-free; filesystem consistency is the kernel's job.
type Filesystem struct {
	root string

	// repoLocks provides exclusive write access per repo name.
	repoLocks sync.Map // map[string]chan struct{}
}

// NewFilesystem validates root exists (or creates it) and returns a
// Filesystem ready for use. The directory layout is created on demand.
func NewFilesystem(root string) (*Filesystem, error) {
	if root == "" {
		return nil, errors.New("storage: empty root path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("storage: create root %s: %w", root, err)
	}
	return &Filesystem{root: root}, nil
}

// Root returns the absolute filesystem root. Used by tests to clean up.
func (f *Filesystem) Root() string { return f.root }

// --- catalog ---------------------------------------------------------------

// Repositories lists every repository under repos/. Repository names contain
// slashes (docker.io/library/nginx), so the tree nests — walk it and treat any
// directory that holds a tags/ or manifests/ subdir as a repository. Parent and
// child can both be repositories (a/b and a/b/c are independent names).
func (f *Filesystem) Repositories(_ context.Context) ([]string, error) {
	repos, err := f.discoverRepos()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.Name)
	}
	return out, nil
}

// repoEntry is one repository directory discovered under f.root/repos.
// Name is slash-separated to match the on-disk layout (e.g. "library/alpine"
// for repos/library/alpine/), Dir is the absolute path of that directory.
type repoEntry struct {
	Name string
	Dir  string
}

// discoverRepos walks repos/ and returns every repository directory found,
// using the same rule as the original Repositories(): a directory counts as
// a repo if it directly contains a `tags/` or `manifests/` subdirectory.
// Names that map to the same repo are deduplicated via a set.
//
// v0.5.24: extracted from Repositories() so GC's Pass 3 can reuse it. The
// previous Pass 3 implementation called os.ReadDir(reposRoot) directly and
// treated every top-level entry as a repo — under multi-segment repos like
// `library/alpine` that meant checking `repos/library/tags/` (which never
// exists), seeing ENOENT, concluding "no tags", and then os.RemoveAll-ing
// `repos/library/` recursively, which silently nuked every repo nested
// under any namespace directory. Critical data loss bug; see CHANGELOG
// 0.5.24.
func (f *Filesystem) discoverRepos() ([]repoEntry, error) {
	reposDir := filepath.Join(f.root, "repos")
	if _, err := os.Stat(reposDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]struct{}{}
	err := filepath.WalkDir(reposDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || !d.IsDir() {
			return nil // best-effort: skip unreadable subtrees
		}
		if p == reposDir {
			return nil
		}
		if n := d.Name(); n != "tags" && n != "manifests" {
			return nil
		}
		rel, relErr := filepath.Rel(reposDir, filepath.Dir(p))
		if relErr != nil || rel == "." {
			return nil
		}
		seen[filepath.ToSlash(rel)] = struct{}{}
		// tags/ and manifests/ never contain nested repos, and pruning here
		// keeps the walk off the (potentially huge) manifest trees.
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]repoEntry, 0, len(names))
	for _, n := range names {
		out = append(out, repoEntry{Name: n, Dir: filepath.Join(reposDir, n)})
	}
	return out, nil
}

func (f *Filesystem) Tags(_ context.Context, repo string) ([]string, error) {
	tagsDir := filepath.Join(f.root, "repos", repo, "tags")
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		// v0.6.12 (REG-2): distinguish "repo doesn't exist" (the parent
		// repos/<repo> directory is missing) from "repo exists but has no
		// tags" (the tags subdir happens to be empty). Before this fix both
		// collapsed to nil, nil and the registryd layer rendered 200 + [] —
		// indistinguishable from a real empty repo. Callers couldn't tell
		// a typo from a clean checkout.
		//
		// We still tolerate the case where the parent repos/<repo> exists
		// but tags/ doesn't (a brand-new repo before any manifest push):
		// that's the legitimate "repo exists, []" answer and stays
		// (nil, nil). Only an absent repos/<repo> parent propagates.
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(filepath.Join(f.root, "repos", repo)); statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					return nil, ErrNotFound
				}
				return nil, statErr
			}
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func (f *Filesystem) TagDigest(_ context.Context, repo, tag string) (string, error) {
	p := filepath.Join(f.root, "repos", repo, "tags", tag)
	body, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	d := strings.TrimSpace(string(body))
	if !looksLikeDigest(d) {
		return "", fmt.Errorf("storage: corrupted tag file %s: %q", p, d)
	}
	return d, nil
}

// --- manifest --------------------------------------------------------------

func (f *Filesystem) GetManifest(_ context.Context, repo, ref string) (*Manifest, error) {
	digest := ref
	if !looksLikeDigest(ref) {
		// ref is a tag; resolve via tags/<ref>
		d, err := f.TagDigest(context.Background(), repo, ref)
		if err != nil {
			return nil, err
		}
		digest = d
	}
	p := f.manifestPath(repo, digest)
	body, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Verify digest matches body. If not, the FS is corrupted; refuse to
	// serve (this is a defense against tampering and bit-rot).
	sum := sha256.Sum256(body)
	if actual := "sha256:" + hex.EncodeToString(sum[:]); actual != digest {
		return nil, fmt.Errorf("storage: manifest %s body digest mismatch (expected %s, got %s)", p, digest, actual)
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	// Read media type from sibling file; default to application/octet-stream
	// if missing.
	mt := readMediaType(p)
	return &Manifest{
		Repo:      repo,
		Digest:    digest,
		MediaType: mt,
		Body:      body,
		CreatedAt: st.ModTime().UTC(),
	}, nil
}

// PutManifest writes body to manifest storage and either updates (if ref
// is a tag) or validates (if ref is a digest) the reference. Returns the
// computed digest.
func (f *Filesystem) PutManifest(_ context.Context, repo, ref, mediaType string, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	if looksLikeDigest(ref) && ref != digest {
		return "", fmt.Errorf("storage: ref digest %s doesn't match body digest %s", ref, digest)
	}

	unlock := f.lockRepo(repo)
	defer unlock()

	// Write manifest file.
	mPath := f.manifestPath(repo, digest)
	if err := os.MkdirAll(filepath.Dir(mPath), 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(mPath, body, 0o644); err != nil {
		return "", err
	}
	if mediaType != "" {
		_ = atomicWrite(mediaTypePath(mPath), []byte(mediaType), 0o644)
	}

	// Update tag if ref is a tag (not a digest).
	if !looksLikeDigest(ref) {
		tagPath := filepath.Join(f.root, "repos", repo, "tags", ref)
		if err := os.MkdirAll(filepath.Dir(tagPath), 0o755); err != nil {
			return "", err
		}
		if err := atomicWrite(tagPath, []byte(digest), 0o644); err != nil {
			return "", err
		}
	}
	return digest, nil
}

func (f *Filesystem) DeleteManifest(_ context.Context, repo, digest string) ([]string, error) {
	if !looksLikeDigest(digest) {
		return nil, ErrInvalidDigest
	}
	unlock := f.lockRepo(repo)
	defer unlock()

	p := f.manifestPath(repo, digest)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = os.Remove(mediaTypePath(p))
	// Drop the now-empty <algo>/<hex>/ husk so repeated push/delete cycles
	// don't leave thousands of empty directories behind.
	_ = os.Remove(filepath.Dir(p))
	// One digest can be referenced by several tags. Leaving them behind would
	// make tags/list advertise a tag whose manifest 404s, so dereference them.
	affected := f.tagsForDigest(repo, digest)
	f.pruneTagsForDigest(repo, digest)
	return affected, nil
}

// tagsForDigest returns every tag under repos/<repo>/tags whose content
// equals digest. The repository lock must already be held by the caller.
func (f *Filesystem) tagsForDigest(repo, digest string) []string {
	tagsDir := filepath.Join(f.root, "repos", repo, "tags")
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(tagsDir, e.Name())
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(body)) == digest {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// TagsForDigest is the Storage interface entry point. It re-acquires the
// repository lock so it can be called independently of DeleteManifest.
func (f *Filesystem) TagsForDigest(_ context.Context, repo, digest string) ([]string, error) {
	if !looksLikeDigest(digest) {
		return nil, ErrInvalidDigest
	}
	unlock := f.lockRepo(repo)
	defer unlock()
	return f.tagsForDigest(repo, digest), nil
}

// pruneTagsForDigest deletes every tag file under repos/<repo>/tags whose
// content equals digest. Best-effort: the caller's manifest delete already
// succeeded, so I/O errors here must not turn into a 500.
func (f *Filesystem) pruneTagsForDigest(repo, digest string) {
	tagsDir := filepath.Join(f.root, "repos", repo, "tags")
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(tagsDir, e.Name())
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(body)) == digest {
			_ = os.Remove(p)
		}
	}
}

// --- blob ------------------------------------------------------------------

func (f *Filesystem) BlobExists(_ context.Context, repo, digest string) (bool, error) {
	if !looksLikeDigest(digest) {
		return false, ErrInvalidDigest
	}
	_, err := os.Stat(f.blobPath(digest))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *Filesystem) GetBlob(_ context.Context, repo, digest string) (io.ReadCloser, int64, error) {
	if !looksLikeDigest(digest) {
		return nil, 0, ErrInvalidDigest
	}
	p := f.blobPath(digest)
	fp, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	st, err := fp.Stat()
	if err != nil {
		fp.Close()
		return nil, 0, err
	}
	return fp, st.Size(), nil
}

func (f *Filesystem) StatBlob(_ context.Context, digest string) (int64, error) {
	if !looksLikeDigest(digest) {
		return 0, ErrInvalidDigest
	}
	st, err := os.Stat(f.blobPath(digest))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return st.Size(), nil
}

// --- upload ----------------------------------------------------------------

func (f *Filesystem) StartUpload(_ context.Context, repo string) (string, error) {
	uuid, err := newUUID()
	if err != nil {
		return "", err
	}
	dir := f.uploadPath(repo, uuid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Write a marker file with the start time so we can GC abandoned sessions.
	startedAt := time.Now().UTC().Format(time.RFC3339)
	if err := atomicWrite(filepath.Join(dir, "startedat"), []byte(startedAt), 0o644); err != nil {
		return "", err
	}
	return uuid, nil
}

func (f *Filesystem) PatchUpload(_ context.Context, repo, uuid string, offset int64, body io.Reader) (int64, error) {
	dir := f.uploadPath(repo, uuid)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	dataPath := filepath.Join(dir, "data")
	fp, err := os.OpenFile(dataPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer fp.Close()

	// If client sent a specific Content-Range offset, honour it by seeking.
	// Content-Range is "bytes START-END"; we treat offset = START.
	if offset > 0 {
		if _, err := fp.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}
	if _, err := io.Copy(fp, body); err != nil {
		return 0, err
	}
	st, err := fp.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (f *Filesystem) PutUpload(_ context.Context, repo, uuid, digest string) error {
	if !looksLikeDigest(digest) {
		return ErrInvalidDigest
	}
	dir := f.uploadPath(repo, uuid)
	dataPath := filepath.Join(dir, "data")
	fp, err := os.Open(dataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	defer fp.Close()

	// Stream-read + sha256 in one pass; we never load the full body.
	h := sha256.New()
	st, err := fp.Stat()
	if err != nil {
		return err
	}
	if _, err := io.Copy(h, fp); err != nil {
		return err
	}
	actual := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if actual != digest {
		return fmt.Errorf("storage: upload body digest mismatch (declared %s, computed %s)", digest, actual)
	}

	// Move the bytes into blob storage atomically. If the blob already
	// exists at the target path, the rename fails (EXDEV) — we just drop
	// the upload dir; the existing blob is fine.
	blobPath := f.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(blobPath); err == nil {
		// blob already exists; just discard the upload dir
		_ = os.RemoveAll(dir)
		return nil
	}
	if err := os.Rename(dataPath, blobPath); err != nil {
		// If rename failed because target exists now, that's fine too.
		if _, statErr := os.Stat(blobPath); statErr == nil {
			_ = os.RemoveAll(dir)
			return nil
		}
		return err
	}
	_ = os.Remove(mediaTypePath(dataPath))
	_ = os.RemoveAll(dir)
	_ = st // keep linter happy if we add size-based logic later
	return nil
}

func (f *Filesystem) GetUpload(_ context.Context, repo, uuid string) (*Upload, error) {
	dir := f.uploadPath(repo, uuid)
	st, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	dataPath := filepath.Join(dir, "data")
	dst, err := os.Stat(dataPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	size := int64(0)
	if dst != nil {
		size = dst.Size()
	}
	return &Upload{
		UUID:      uuid,
		Repo:      repo,
		StartedAt: st.ModTime().UTC(),
		Size:      size,
	}, nil
}

func (f *Filesystem) CancelUpload(_ context.Context, repo, uuid string) error {
	dir := f.uploadPath(repo, uuid)
	if err := os.RemoveAll(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// --- stats -----------------------------------------------------------------

func (f *Filesystem) Stats(_ context.Context) (*StorageStats, error) {
	s := &StorageStats{}
	repos, err := f.Repositories(context.Background())
	if err != nil {
		return nil, err
	}
	s.RepoCount = len(repos)
	for _, r := range repos {
		tags, err := f.Tags(context.Background(), r)
		if err != nil {
			continue
		}
		s.TagCount += len(tags)
	}
	// Count blobs (walks the sha256/ tree).
	blobsRoot := filepath.Join(f.root, "blobs", "sha256")
	_ = filepath.Walk(blobsRoot, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "data" {
			s.BlobCount++
			s.TotalSize += info.Size()
		}
		return nil
	})
	// Recount manifests separately (they live under repos/.../manifests/).
	_ = filepath.Walk(filepath.Join(f.root, "repos"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "data" {
			s.ManifestCount++
		}
		return nil
	})
	return s, nil
}

// --- paths & helpers -------------------------------------------------------

func (f *Filesystem) manifestPath(repo, digest string) string {
	algo, hexPart := splitDigest(digest)
	return filepath.Join(f.root, "repos", repo, "manifests", algo, hexPart, "data")
}

func (f *Filesystem) blobPath(digest string) string {
	algo, hexPart := splitDigest(digest)
	prefix := hexPart[:2]
	return filepath.Join(f.root, "blobs", algo, prefix, hexPart, "data")
}

func (f *Filesystem) uploadPath(repo, uuid string) string {
	return filepath.Join(f.root, "uploads", repo, uuid)
}

func (f *Filesystem) lockRepo(repo string) func() {
	v, _ := f.repoLocks.LoadOrStore(repo, make(chan struct{}, 1))
	ch := v.(chan struct{})
	ch <- struct{}{}
	return func() { <-ch }
}

func splitDigest(digest string) (algo, hexPart string) {
	if !looksLikeDigest(digest) {
		return "sha256", ""
	}
	parts := strings.SplitN(digest, ":", 2)
	return parts[0], parts[1]
}

func looksLikeDigest(d string) bool {
	if !strings.HasPrefix(d, "sha256:") {
		return false
	}
	hexPart := d[len("sha256:"):]
	if len(hexPart) != 64 {
		return false
	}
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func mediaTypePath(manifestDataPath string) string {
	return manifestDataPath + ".mediatype"
}

func readMediaType(manifestDataPath string) string {
	body, err := os.ReadFile(mediaTypePath(manifestDataPath))
	if err != nil {
		return "application/octet-stream"
	}
	return strings.TrimSpace(string(body))
}

// atomicWrite writes body to a tmp file in the same dir, fsync, then rename.
// Avoids leaving half-written manifests / tags / blobs if the process is
// killed mid-write.
func atomicWrite(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// newUUID returns a 16-byte random hex (32 chars). We don't use the full
// RFC 4122 format — collisions are vanishingly unlikely at our scale and
// the spec only requires "unique within the registry".
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Ensure JSON import is used (we'll use it for the storage stats later).
var _ = json.Marshal

// --- admin operations ------------------------------------------------------

// reDigest matches the sha256 digests embedded in manifest bodies (config +
// layer descriptors, and child manifests for an index). We scrape the raw
// JSON instead of decoding because a manifest may be any of the four media
// types and we only care about which blobs are still referenced.
var reDigest = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// ManifestDigests lists every manifest stored under repo — including ones no
// tag points at any more (a tag move leaves the old manifest dangling, and
// the UI's inventory should still show it so it can be cleaned up).
func (f *Filesystem) ManifestDigests(_ context.Context, repo string) ([]string, error) {
	base := filepath.Join(f.root, "repos", repo, "manifests", "sha256")
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, e.Name(), "data")); err != nil {
			continue // husk without a body: not a manifest
		}
		out = append(out, "sha256:"+e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// DeleteRepository drops an entire repository: every manifest, tag, and
// in-flight upload session under it. Blobs are content-addressed and shared
// between repositories, so they are left untouched — run GC to reclaim them.
// ExportTar streams a `docker save`-compatible tar archive for one
// tag/digest into w. Implementation lives in export.go so the tar
// layout can be unit-tested against any Storage impl (fake / future
// remote backends) without going through the filesystem. The Filesystem
// implementation here just forwards — every blob read goes through
// f.GetBlob which already does the sha256 verification on Put, so the
// stream contains exactly the bytes that landed on disk.
//
// See export.go for the format reference and the layout choices.
func (f *Filesystem) ExportTar(ctx context.Context, repo, ref string, opt ExportOpt, w io.Writer) (*ExportResult, error) {
	return exportTar(ctx, f, repo, ref, opt, w)
}

func (f *Filesystem) DeleteRepository(_ context.Context, repo string) error {
	if err := cleanRepoName(repo); err != nil {
		return err
	}
	unlock := f.lockRepo(repo)
	defer unlock()

	dir := filepath.Join(f.root, "repos", repo)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(f.root, "uploads", repo))
	pruneEmptyDirs(filepath.Dir(dir), filepath.Join(f.root, "repos"))
	return nil
}

// GC sweeps the store and reclaims disk. Deleting a manifest only drops the
// reference — the blob bytes stay on disk until a sweep runs, which is the
// same contract as `registry garbage-collect` in CNCF Distribution.
//
// Passes:
//
//  1. (a/b) blobs no stored manifest mentions are deleted;
//  2. upload sessions abandoned mid-PUT (>24h) are discarded;
//  3. (v0.5.20, opt-in via opts.CleanEmptyRepos) every repository whose
//     tags/ is empty AND that has no upload session younger than the 24h
//     cutoff has its repos/<repo>/ + uploads/<repo>/ tree removed
//     wholesale. Skips repositories that look empty but are clearly in
//     use, to avoid clobbering an in-flight push.
//  4. (v0.5.20, opt-in) re-runs pass 1: pass 3 deletes orphan manifest
//     bodies whose blobs were "live" only because those manifests existed.
//     A second pass is what actually frees the bytes from those blobs.
func (f *Filesystem) GC(_ context.Context, opts GCOption) (*GCResult, error) {
	res := &GCResult{}

	// Pass 1a/1b: collect live blobs, then drop unreferenced ones. Reused by
	// Pass 4 below; the inline closure over `live` is the same shape both
	// times so a refactor later only touches one site.
	collectLive := func() map[string]struct{} {
		live := map[string]struct{}{}
		reposRoot := filepath.Join(f.root, "repos")
		_ = filepath.Walk(reposRoot, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || info.Name() != "data" {
				return nil
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, d := range reDigest.FindAllString(string(body), -1) {
				live[d] = struct{}{}
			}
			return nil
		})
		return live
	}
	sweepBlobs := func(live map[string]struct{}) (removed int, freed int64) {
		blobsRoot := filepath.Join(f.root, "blobs", "sha256")
		_ = filepath.Walk(blobsRoot, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || info.Name() != "data" {
				return nil
			}
			dir := filepath.Dir(p)
			digest := "sha256:" + filepath.Base(dir)
			if !looksLikeDigest(digest) {
				return nil
			}
			// v0.6.32: in dry-run mode, every walked blob contributes to
			// ScannedBlobs; ones not in `live` also bump OrphanBlobs /
			// OrphanBytes so the UI shows both "how much we walked" and
			// "how much is reclaimable" without ever touching the disk.
			if opts.DryRun {
				res.ScannedBlobs++
				if _, ok := live[digest]; !ok {
					res.OrphanBlobs++
					res.OrphanBytes += info.Size()
				}
				return nil
			}
			if _, ok := live[digest]; ok {
				return nil
			}
			removed++
			freed += info.Size()
			if err := os.Remove(p); err != nil {
				removed--
				freed -= info.Size()
				return nil
			}
			pruneEmptyDirs(dir, blobsRoot)
			return nil
		})
		return removed, freed
	}

	live := collectLive()
	res.RemovedBlobs, res.FreedBytes = sweepBlobs(live)

	// Pass 2: abandon old upload sessions. StartUpload writes a startedat
	// marker precisely so this sweep can tell "in flight" from "client died".
	uploadsRoot := filepath.Join(f.root, "uploads")
	// v0.7.37 (review §1.2): was hardcoded `-24 * time.Hour` here and
	// again in server.go's retentionLoop. Sourced from
	// config.UploadSessionTTL (== config.DayInterval) so both stay in
	// lockstep when one changes.
	cutoff := time.Now().UTC().Add(-config.UploadSessionTTL)
	_ = filepath.Walk(uploadsRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() {
			return nil
		}
		body, err := os.ReadFile(filepath.Join(p, "startedat"))
		if err != nil {
			return nil
		}
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(body)))
		if err != nil || ts.After(cutoff) {
			return nil
		}
		// v0.6.32: dry-run counts but doesn't touch the filesystem.
		if opts.DryRun {
			res.AbandonedUploads++
			return filepath.SkipDir
		}
		_ = os.RemoveAll(p)
		pruneEmptyDirs(filepath.Dir(p), uploadsRoot)
		return filepath.SkipDir
	})

	if !opts.CleanEmptyRepos {
		return res, nil
	}

	// Pass 3: drop empty repository directories. The deletion is destructive
	// enough that we triple-check before acting:
	//
	//   (a) tags/ must be empty (or absent) — protects repositories with
	//       any user-visible tag.
	//   (b) uploads/<repo>/ must hold no session younger than the 24h
	//       cutoff — protects in-flight pushes. Old orphan sessions in
	//       uploads/ were already swept by Pass 2 above.
	//   (c) repo lock per repo — protects against a push racing with us.
	//
	// The size of repos/<repo>/ is summed before deletion so the UI can
	// credit those bytes to the user; the cost is one extra filepath.Walk
	// per candidate repo, which is cheap relative to the existing sweeps.
	// v0.5.24: fix Pass 3 namespace-confusion bug (CHANGELOG 0.5.24).
	//
	// The previous implementation used os.ReadDir(reposRoot) and treated
	// every top-level entry as a repo — under multi-segment repos like
	// library/alpine that meant checking repos/library/tags/ (which never
	// exists), concluding ENOENT = "no tags", and then os.RemoveAll-ing
	// repos/library/ recursively, which silently nuked every repo nested
	// under any namespace directory.
	//
	// Now we use the same discovery rule as Repositories(): walk repos/
	// looking for `tags/` or `manifests/` siblings. Each repo is the parent
	// of those subdirectories — so a `library/alpine` repo is discovered
	// as Name="library/alpine", Dir=repos/library/alpine, and the deletion
	// targets the leaf, not the namespace.
	reposRoot := filepath.Join(f.root, "repos")
	repos, err := f.discoverRepos()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Surface the error so the caller knows the sweep was incomplete
		// rather than silently reporting empty results.
		return res, err
	}
	for _, r := range repos {
		if err := cleanRepoName(r.Name); err != nil {
			continue
		}
		if !repoHasNoTags(r.Dir) {
			continue
		}
		if f.repoHasActiveUpload(r.Name, cutoff) {
			continue
		}
		freed := dirSize(r.Dir) + dirSize(filepath.Join(uploadsRoot, r.Name))
		// v0.6.32: dry-run counts what Pass 3 *would* free but skips
		// the lock + rm-rf + prune so nothing on disk changes. Pass 4
		// is skipped below because it depends on Pass 3 actually
		// deleting (a real Pass 3 removes the manifest link files
		// whose blobs Pass 4 then orphans).
		if opts.DryRun {
			res.WouldRemoveEmptyRepos = append(res.WouldRemoveEmptyRepos, r.Name)
			res.WouldEmptyRepoFreedBytes += freed
			continue
		}
		// From here on we are committed to deleting the repo. Take the lock
		// so a concurrent push can't add a tag mid-flight.
		unlock := f.lockRepo(r.Name)
		if err := os.RemoveAll(r.Dir); err != nil {
			unlock()
			return res, err
		}
		_ = os.RemoveAll(filepath.Join(uploadsRoot, r.Name))
		pruneEmptyDirs(filepath.Dir(r.Dir), reposRoot)
		unlock()
		res.RemovedEmptyRepos = append(res.RemovedEmptyRepos, r.Name)
		res.EmptyRepoFreedBytes += freed
	}

	// Pass 4: re-sweep blobs. The repos we just deleted held manifest bodies
	// whose blobs were "live" only because those manifests existed. Now
	// they're truly orphaned, so a second Pass 1 is what actually frees
	// those bytes. Done in addition to Pass 1 — its count goes on top.
	//
	// v0.6.32: skipped in dry-run mode because Pass 4 needs Pass 3 to have
	// actually deleted the manifest link files. Dry-run reports just Pass 1
	// counts (ScannedBlobs / OrphanBlobs / OrphanBytes), which is the
	// upper bound on what the next real sweep would free from blobs.
	if len(res.RemovedEmptyRepos) > 0 {
		extraBlobs, extraBytes := sweepBlobs(collectLive())
		res.RemovedBlobs += extraBlobs
		res.FreedBytes += extraBytes
	}
	return res, nil
}

// repoHasNoTags reports whether repos/<repo>/tags/ holds no tag files.
// Best-effort: a missing tags/ directory is treated as "no tags". The
// special files (current/ subdirectory, index/) that registry layouts
// keep under tags/ are directories — we skip them, only counting plain
// files (which is what every actual tag is stored as).
func repoHasNoTags(repoDir string) bool {
	tagsDir := filepath.Join(repoDir, "tags")
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return false
		}
	}
	return true
}

// repoHasActiveUpload reports whether uploads/<repo>/ holds any session
// whose startedat is strictly after cutoff. A cutoff of time.Now()-24h
// matches the Pass 2 sweep above, so this is the same notion of "live".
// If uploads/<repo>/ is missing or contains no startedat we can read, we
// say "no active upload" — pass 2 already cleaned the dead ones.
func (f *Filesystem) repoHasActiveUpload(repo string, cutoff time.Time) bool {
	uploadRoot := filepath.Join(f.root, "uploads", repo)
	entries, err := os.ReadDir(uploadRoot)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(uploadRoot, e.Name(), "startedat"))
		if err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(body)))
		if err == nil && ts.After(cutoff) {
			return true
		}
	}
	return false
}

// dirSize walks path and sums every regular file's size. The result is
// approximate (an in-flight writer can change bytes between stat and
// delete) but good enough for the UI's freed-bytes headline. Errors are
// swallowed; a half-failed walk returning 0 is better than blocking the
// GC sweep on a transient read failure.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// cleanRepoName rejects repository paths that would escape the storage root.
// Multi-segment names (docker.io/library/nginx) are fine; empty / "." / ".."
// segments are not.
func cleanRepoName(repo string) error {
	if repo == "" || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return ErrNotFound
	}
	for _, seg := range strings.Split(repo, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ErrNotFound
		}
	}
	return nil
}

// pruneEmptyDirs removes dir and its ancestors while they stay empty, never
// ascending above stop. After a delete this clears the husks left behind
// (blobs/sha256/aa/, repos/a/) without ever touching the roots themselves.
func pruneEmptyDirs(dir, stop string) {
	stop = filepath.Clean(stop)
	for {
		dir = filepath.Clean(dir)
		if dir == stop || !strings.HasPrefix(dir, stop+string(filepath.Separator)) {
			return
		}
		if err := os.Remove(dir); err != nil {
			return // not empty, or already gone: stop here
		}
		dir = filepath.Dir(dir)
	}
}

var _ = time.Now
