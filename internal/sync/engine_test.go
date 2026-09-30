package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// fakeSource is a minimal V2 distribution registry for tests. It serves
// _catalog, tags/list, manifests, and blobs over HTTP. State is keyed
// by (repo, tag) for manifests and by sha256 digest for blobs.
//
// The fake does NOT implement auth — sync engine tests for credential
// resolution live separately (TestEngine_UnknownCredentialFailsLoudly).
type fakeSource struct {
	mu      sync.Mutex
	repos   map[string]*fakeRepo
	blobs   map[string][]byte
	server  *httptest.Server
	baseURL string
}

type fakeRepo struct {
	tags map[string]*fakeManifest
}

type fakeManifest struct {
	digest string
	raw    []byte
}

func newFakeSource(t *testing.T) *fakeSource {
	t.Helper()
	s := &fakeSource{
		repos: map[string]*fakeRepo{},
		blobs: map[string][]byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", s.handle)
	s.server = httptest.NewServer(mux)
	s.baseURL = s.server.URL
	return s
}

func (s *fakeSource) Close()                 { s.server.Close() }
func (s *fakeSource) URL() string             { return s.baseURL }

// AddImage stores one (repo, tag) with the given config + layer blobs.
// Layer + config bytes are arbitrary; they're hashed into sha256
// digests the registry serves back.
func (s *fakeSource) AddImage(t *testing.T, repo, tag string, configContent, layerContent []byte) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	cfgD := digestOf(configContent)
	lyrD := digestOf(layerContent)

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
		"config": map[string]any{
			"mediaType": "application/vnd.docker.container.image.v1+json",
			"digest":    cfgD,
			"size":      int64(len(configContent)),
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
				"digest":    lyrD,
				"size":      int64(len(layerContent)),
			},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	md := digestOf(raw)

	r := s.repos[repo]
	if r == nil {
		r = &fakeRepo{tags: map[string]*fakeManifest{}}
		s.repos[repo] = r
	}
	r.tags[tag] = &fakeManifest{digest: md, raw: raw}

	s.blobs[cfgD] = configContent
	s.blobs[lyrD] = layerContent
	s.blobs[md] = raw
}

// handle dispatches _catalog, tags/list, manifests/<ref>, blobs/<digest>.
// Repos can be multi-segment ("library/nginx", "team-a/web"), so we
// split on "/" and detect the "kind" by the second-to-last segment.
func (s *fakeSource) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	parts := strings.Split(p, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	// _catalog
	if len(parts) == 1 && parts[0] == "_catalog" {
		names := make([]string, 0, len(s.repos))
		for n := range s.repos {
			names = append(names, n)
		}
		writeJSON(w, map[string]any{"repositories": names})
		return
	}
	if len(parts) < 3 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	kind := parts[len(parts)-2]
	ref := parts[len(parts)-1]
	repo := strings.Join(parts[:len(parts)-2], "/")

	rec := s.repos[repo]
	if rec == nil {
		http.Error(w, "no such repo", http.StatusNotFound)
		return
	}
	switch kind {
	case "tags":
		if !strings.HasPrefix(ref, "list") {
			http.Error(w, "bad tags path", http.StatusBadRequest)
			return
		}
		tagNames := make([]string, 0, len(rec.tags))
		for tn := range rec.tags {
			tagNames = append(tagNames, tn)
		}
		writeJSON(w, map[string]any{"name": repo, "tags": tagNames})
	case "manifests":
		m, ok := rec.tags[ref]
		if !ok {
			http.Error(w, "no such tag", http.StatusNotFound)
			return
		}
		mt := "application/vnd.docker.distribution.manifest.v2+json"
		w.Header().Set("Content-Type", mt)
		w.Header().Set("Docker-Content-Digest", m.digest)
		_, _ = w.Write(m.raw)
	case "blobs":
		// ref may be "uploads/<uuid>" (PATCH) or just a digest.
		d := strings.TrimPrefix(ref, "uploads/")
		if u, err := url.Parse(ref); err == nil {
			if qd := u.Query().Get("digest"); qd != "" {
				d = qd
			}
		}
		body, ok := s.blobs[d]
		if !ok {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Docker-Content-Digest", d)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	default:
		http.Error(w, "unknown kind", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// localStore spins up a real storage.Filesystem in a tmp dir. Tests use
// this so engine code paths exercise the same code as production (no
// in-memory shim that could drift from real behaviour).
func localStore(t *testing.T) storage.Storage {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewFilesystem(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatalf("storage.NewFilesystem: %v", err)
	}
	return s
}

// newTestEngine wires the minimum engine for pull-direction tests:
// NewClient uses registry.NewClient directly (no proxy, no auth —
// credentials are tested separately).
func newTestEngine(t *testing.T, srcURL string, local storage.Storage) *Engine {
	t.Helper()
	return NewEngine(local, func(_ context.Context, baseURL, user, pass string) (*registry.Client, error) {
		c, err := registry.NewClient(registry.Config{BaseURL: baseURL, Username: user, Password: pass})
		if err != nil {
			return nil, err
		}
		return c, nil
	}, nil, nil)
}

// newTestDB opens a tmp SQLite database. Each test gets its own so the
// migration runs cleanly and tasks/runs from one test don't bleed into
// another.
func newTestDB(t *testing.T) *db.Db {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// --- Tests ---------------------------------------------------------------

func TestDenyFilter(t *testing.T) {
	cases := []struct {
		name string
		deny []string
		repo string
		want bool
	}{
		{"empty denies nothing", nil, "team-a/web", true},
		{"exact match blocked", []string{"team-a/web"}, "team-a/web", false},
		{"different repo allowed", []string{"team-a/web"}, "team-a/api", true},
		{"case-sensitive", []string{"team-a/web"}, "Team-A/Web", true},
		{"whitespace trimmed", []string{"  team-a/web  "}, "team-a/web", false},
		{"empty entry skipped", []string{"", "team-a/web"}, "team-a/web", false},
		{"empty entry not blocking", []string{""}, "team-a/web", true},
		{"substring not blocked", []string{"team-a"}, "team-a/web", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := NewDenyFilter(c.deny)
			if got := f.Allows(c.repo); got != c.want {
				t.Fatalf("Allows(%q) = %v, want %v (deny=%v)", c.repo, got, c.want, c.deny)
			}
		})
	}
}

func TestPrefixFilter(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		repo   string
		want   bool
	}{
		{"empty allows all", "", "anything/goes", true},
		{"matching prefix", "team-a/", "team-a/web", true},
		{"prefix without trailing slash", "team-a", "team-a/web", true},
		{"different prefix blocked", "team-b/", "team-a/web", false},
		{"short substring prefix matches via HasPrefix", "team", "team-a/web", true},
		{"partial-segment prefix not matched", "team-a", "team-a-extra/web", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := NewPrefixFilter(c.prefix)
			if got := f.Allows(c.repo); got != c.want {
				t.Fatalf("Allows(%q) = %v, want %v (prefix=%q)", c.repo, got, c.want, c.prefix)
			}
		})
	}
}

func TestStore_TaskCRUD(t *testing.T) {
	d := newTestDB(t)
	s := NewStore(d)
	ctx := context.Background()

	created, err := s.CreateTask(ctx, Task{
		ID:        "t-1",
		Name:      "nightly-nginx",
		Direction: DirectionPull,
		SourceURL: "https://registry-1.docker.io",
		DenyList:  []string{"library/redis"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.ID != "t-1" || created.Name != "nightly-nginx" {
		t.Fatalf("created task shape wrong: %+v", created)
	}
	if len(created.DenyList) != 1 || created.DenyList[0] != "library/redis" {
		t.Fatalf("deny list round-trip: %v", created.DenyList)
	}

	got, err := s.GetTask(ctx, "t-1")
	if err != nil || got.ID != "t-1" {
		t.Fatalf("GetTask: %v %+v", err, got)
	}

	// Update changes editable fields, preserves last_run_*.
	got.SourceRepoPrefix = "library/"
	got.TargetRepoPrefix = "mirrored/"
	got.DenyList = []string{"library/redis", "library/busybox"}
	upd, err := s.UpdateTask(ctx, got)
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if upd.SourceRepoPrefix != "library/" || upd.TargetRepoPrefix != "mirrored/" {
		t.Fatalf("update didn't apply: %+v", upd)
	}
	if len(upd.DenyList) != 2 {
		t.Fatalf("deny list update: %v", upd.DenyList)
	}

	// Run lifecycle: start + finish should populate counts.
	if err := s.RecordRunStart(ctx, Run{
		ID:        "r-1",
		TaskID:    "t-1",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordRunStart: %v", err)
	}
	if err := s.RecordRunFinish(ctx, Run{
		ID:              "r-1",
		TaskID:          "t-1",
		State:           RunStateSuccess,
		StartedAt:       time.Now().UTC().Add(-time.Second),
		FinishedAt:      time.Now().UTC(),
		ManifestsCopied: 3,
		BlobsCopied:     7,
		BlobsSkipped:    1,
		BytesTotal:      12345,
	}); err != nil {
		t.Fatalf("RecordRunFinish: %v", err)
	}
	final, err := s.GetTask(ctx, "t-1")
	if err != nil {
		t.Fatalf("GetTask after run: %v", err)
	}
	if final.LastRunStatus != "success" {
		t.Fatalf("last_run_status not denormalised: %q", final.LastRunStatus)
	}

	runs, err := s.ListRuns(ctx, "t-1", 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].ManifestsCopied != 3 || runs[0].BlobsCopied != 7 {
		t.Fatalf("counts not persisted: %+v", runs[0])
	}

	// Delete cascades to runs.
	if _, err := s.DeleteTask(ctx, "t-1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	runs, _ = s.ListRuns(ctx, "t-1", 0)
	if len(runs) != 0 {
		t.Fatalf("expected cascade delete of runs, got %d", len(runs))
	}
}

func TestStore_NameConflict(t *testing.T) {
	d := newTestDB(t)
	s := NewStore(d)
	ctx := context.Background()
	if _, err := s.CreateTask(ctx, Task{ID: "t-a", Name: "same", Direction: DirectionPull, SourceURL: "x"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.CreateTask(ctx, Task{ID: "t-b", Name: "same", Direction: DirectionPull, SourceURL: "y"}); err == nil {
		t.Fatal("expected unique-name violation")
	}
}

func TestEngine_PullCopiesOneImage(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "library/nginx", "1.25", []byte("config-json"), []byte("layer-tar-gz"))
	src.AddImage(t, "library/nginx", "1.26", []byte("config-json"), []byte("layer-tar-gz-v2"))

	local := localStore(t)
	eng := newTestEngine(t, src.URL(), local)

	run, err := eng.Run(context.Background(), Task{
		ID:        "t-1",
		Name:      "x",
		Direction: DirectionPull,
		SourceURL: src.URL(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.State != RunStateSuccess {
		t.Fatalf("state = %q, want success; err=%q", run.State, run.Error)
	}
	if run.ManifestsCopied != 2 {
		t.Fatalf("manifests copied = %d, want 2 (two tags)", run.ManifestsCopied)
	}
	// First tag copies 2 (config + layer), second tag copies only the
	// new layer (config digest is already in local store from tag 1).
	// This matches BlobExists behaviour — once a blob lands, all later
	// (repo, tag) pairs skip it for free.
	if run.BlobsCopied != 3 {
		t.Fatalf("blobs copied = %d, want 3 (2 from 1.25 + 1 new layer from 1.26)", run.BlobsCopied)
	}
	if run.BlobsSkipped != 1 {
		t.Fatalf("blobs skipped = %d, want 1 (shared config blob)", run.BlobsSkipped)
	}
}

func TestEngine_PullIdempotentSecondRun(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "library/nginx", "1.25", []byte("config"), []byte("layer"))

	local := localStore(t)
	eng := newTestEngine(t, src.URL(), local)
	task := Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
	}

	first, err := eng.Run(context.Background(), task)
	if err != nil || first.State != RunStateSuccess {
		t.Fatalf("first run: err=%v state=%q", err, first.State)
	}
	if first.BlobsCopied != 2 {
		t.Fatalf("first run blobs copied = %d, want 2", first.BlobsCopied)
	}

	second, err := eng.Run(context.Background(), task)
	if err != nil || second.State != RunStateSuccess {
		t.Fatalf("second run: err=%v state=%q", err, second.State)
	}
	if second.BlobsCopied != 0 {
		t.Fatalf("second run should not copy any blobs, got %d", second.BlobsCopied)
	}
	if second.BlobsSkipped != 2 {
		t.Fatalf("second run should skip 2 blobs, got %d", second.BlobsSkipped)
	}
	// Manifests are always PUT — engine counts them regardless. PUT is
	// idempotent on the storage layer (same digest → same path).
	if second.ManifestsCopied != 1 {
		t.Fatalf("second run manifests copied = %d, want 1", second.ManifestsCopied)
	}
}

func TestEngine_PullAppliesDenyList(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "team-a/web", "v1", []byte("c"), []byte("l"))
	src.AddImage(t, "team-a/api", "v1", []byte("c"), []byte("l"))
	src.AddImage(t, "team-b/db", "v1", []byte("c"), []byte("l"))

	local := localStore(t)
	eng := newTestEngine(t, src.URL(), local)

	run, err := eng.Run(context.Background(), Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
		DenyList: []string{"team-a/api"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.State != RunStateSuccess {
		t.Fatalf("state = %q, want success", run.State)
	}
	if run.ManifestsCopied != 2 {
		t.Fatalf("manifests copied = %d, want 2 (web + db, NOT api)", run.ManifestsCopied)
	}
}

func TestEngine_PullAppliesSourceRepoPrefix(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "library/alpine", "3.19", []byte("c"), []byte("l"))
	src.AddImage(t, "team/nginx", "1.0", []byte("c"), []byte("l"))

	local := localStore(t)
	eng := newTestEngine(t, src.URL(), local)

	run, err := eng.Run(context.Background(), Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
		SourceRepoPrefix: "library/",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.ManifestsCopied != 1 {
		t.Fatalf("manifests copied = %d, want 1 (only library/alpine)", run.ManifestsCopied)
	}
}

func TestEngine_PullTargetRepoPrefixMapping(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "team-a/web", "v1", []byte("c"), []byte("l"))

	local := localStore(t)
	eng := newTestEngine(t, src.URL(), local)

	_, err := eng.Run(context.Background(), Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
		TargetRepoPrefix: "mirrored/",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Assert: the local store has the manifest under mirrored/team-a/web.
	ctx := context.Background()
	if _, err := local.GetManifest(ctx, "mirrored/team-a/web", "v1"); err != nil {
		t.Fatalf("expected manifest under mirrored/team-a/web:v1, got %v", err)
	}
	// And NOT under the unmapped name.
	if _, err := local.GetManifest(ctx, "team-a/web", "v1"); err == nil {
		t.Fatal("did not expect unmapped manifest")
	}
}

func TestEngine_UnknownCredentialFailsLoudly(t *testing.T) {
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "team/x", "1", []byte("c"), []byte("l"))

	local := localStore(t)
	resolver := func(_ context.Context, id string) (string, string, error) {
		return "", "", fmt.Errorf("credential %q not found", id)
	}
	eng := NewEngine(local, func(_ context.Context, baseURL, user, pass string) (*registry.Client, error) {
		return registry.NewClient(registry.Config{BaseURL: baseURL, Username: user, Password: pass})
	}, resolver, nil)

	run, err := eng.Run(context.Background(), Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
		CredentialID: "missing-id",
	})
	if err == nil {
		t.Fatal("expected error from missing credential")
	}
	if run.State != RunStateFailed {
		t.Fatalf("state = %q, want failed", run.State)
	}
	if !strings.Contains(run.Error, "missing-id") {
		t.Fatalf("error didn't mention missing id: %q", run.Error)
	}
	if run.ManifestsCopied != 0 {
		t.Fatalf("manifests copied should be 0, got %d", run.ManifestsCopied)
	}
}

func TestEngine_AnonymousCredentialNoResolver(t *testing.T) {
	// Tasks without credential_id should work even when no resolver is
	// wired — anonymous pull is the default for public sources.
	src := newFakeSource(t)
	defer src.Close()
	src.AddImage(t, "library/alpine", "3.19", []byte("c"), []byte("l"))

	local := localStore(t)
	eng := NewEngine(local, func(_ context.Context, baseURL, user, pass string) (*registry.Client, error) {
		return registry.NewClient(registry.Config{BaseURL: baseURL, Username: user, Password: pass})
	}, nil, nil)

	run, err := eng.Run(context.Background(), Task{
		ID: "t-1", Name: "x", Direction: DirectionPull, SourceURL: src.URL(),
		CredentialID: "", // anonymous
	})
	if err != nil || run.State != RunStateSuccess {
		t.Fatalf("anonymous run failed: err=%v state=%q errMsg=%q", err, run.State, run.Error)
	}
}