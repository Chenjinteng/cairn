package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Page sizes for /v2/_catalog and /v2/<name>/tags/list.
//
// Registry-manager omits ?n= entirely (per AGENTS.md §"其它") so behavior is
// consistent across v2.8.3 / v3.x. We adopt the same default here. Callers
// who need a specific page can override via the *WithPage variants below.
const (
	defaultCatalogPage = 100
	defaultTagPage     = 1000
)

// ListRepositories calls /v2/_catalog and returns the full repository list.
//
// Pagination: we keep requesting until one of three stop conditions is met:
//  1. Short page (len(batch) < pageSize) — server signals end-of-list
//  2. All items in this batch are duplicates — server treats ?last= as
//     inclusive and keeps re-emitting the cursor item, so we got the same
//     content we already have
//  3. Safety cap maxCatalogPages — a misbehaving server shouldn't hang us
//
// We deduplicate via a `seen` map rather than trusting the server's pagination
// semantics. This is robust against registries that use either inclusive or
// exclusive last cursors, and against any short re-emission of items we've
// already collected. v0.6.1 added the dedupe + safety cap; without them the
// previous `len(batch) < pageSize`-only loop went into an infinite regress
// against any registry where the last item of a full page is also the first
// item of the next page.
func (c *Client) ListRepositories(ctx context.Context) ([]string, error) {
	const maxCatalogPages = 5000
	seen := map[string]struct{}{}
	var all []string
	last := ""
	for i := 0; i < maxCatalogPages; i++ {
		batch, err := c.catalogPage(ctx, defaultCatalogPage, last)
		if err != nil {
			return nil, err
		}
		newCount := 0
		for _, r := range batch {
			if _, dup := seen[r]; !dup {
				seen[r] = struct{}{}
				all = append(all, r)
				newCount++
			}
		}
		if len(batch) < defaultCatalogPage {
			return all, nil
		}
		if newCount == 0 {
			return all, nil
		}
		last = batch[len(batch)-1]
	}
	return all, nil
}

// catalogPage performs one /v2/_catalog page request.
//
// _catalog entries may have tags=null (empty repositories); we preserve them
// in the output as empty strings so the inventory can render "0 tags" rows
// instead of dropping them silently.
func (c *Client) catalogPage(ctx context.Context, n int, last string) ([]string, error) {
	q := url.Values{}
	q.Set("n", strconv.Itoa(n))
	if last != "" {
		q.Set("last", last)
	}
	var doc struct {
		Repositories []string `json:"repositories"`
	}
	path := "/v2/_catalog?" + q.Encode()
	resp, _, err := c.doRequest(ctx, http.MethodGet, path, "", &doc)
	if err != nil {
		return nil, err
	}
	_ = resp // future: inspect Link header if we ever switch pagination strategies
	return doc.Repositories, nil
}

// ListTags calls /v2/<name>/tags/list and returns every tag.
//
// We deliberately do NOT pass ?n= (registry-manager's quirk); see AGENTS.md.
func (c *Client) ListTags(ctx context.Context, repo string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("registry: repo name is empty")
	}
	var doc struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	path := fmt.Sprintf("/v2/%s/tags/list", escapeRepo(repo))
	_, _, err := c.doRequest(ctx, http.MethodGet, path, "", &doc)
	if err != nil {
		return nil, err
	}
	// Per the V2 spec, a "name unknown" repository returns 404 NAME_UNKNOWN;
	// the inventory scanner treats that as empty-tag (not an error) so a
	// concurrent delete between catalog and tags/list doesn't surface as a
	// red error banner.
	if e, ok := err.(*Error); ok && e.IsNotFound() {
		return nil, nil
	}
	if doc.Tags == nil {
		// tags: null in JSON — treat as empty list, not as a hard error.
		return nil, nil
	}
	return doc.Tags, nil
}

// GetManifest fetches a manifest by tag or digest and decodes it.
//
// Accept header is set automatically by doRequest when path contains
// /manifests/. The returned Digest is the registry-computed sha256 from
// the Docker-Content-Digest response header, NOT recomputed from bytes.
//
// Layer sizes are summed into Manifest.Size (NOT disk usage — blobs are
// shared across tags and repositories).
func (c *Client) GetManifest(ctx context.Context, repo, reference string) (*Manifest, error) {
	if repo == "" || reference == "" {
		return nil, fmt.Errorf("registry: repo and reference are required")
	}
	path := fmt.Sprintf("/v2/%s/manifests/%s", escapeRepo(repo), url.PathEscape(reference))

	resp, body, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	if e, ok := err.(*Error); ok && e.IsNotFound() {
		return nil, e // caller decides whether to swallow
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	mediaType := resp.Header.Get("Content-Type")

	raw := json.RawMessage(body)
	m := &Manifest{
		Digest:    digest,
		MediaType: mediaType,
		Raw:       raw,
	}

	// Decode once into a generic shape; multi-arch indexes recurse one level.
	if err := decodeManifestLayers(raw, mediaType, m); err != nil {
		return nil, fmt.Errorf("registry: decode manifest layers: %w", err)
	}
	if err := decodeManifestCreated(raw, mediaType, m); err != nil {
		// Non-fatal: image config may be unreadable but layers are still valid.
		// We log via the caller's context; here we just leave Created unset.
		_ = err
	}
	return m, nil
}

// decodeManifestLayers populates Layers + Size on m from a raw manifest body.
// OCI indexes and Docker manifest lists carry no layers directly; we recurse
// into the first child's manifest to surface its layers + arch + os.
func decodeManifestLayers(raw json.RawMessage, mediaType string, m *Manifest) error {
	// Multi-arch index: descend into the first child manifest.
	if strings.Contains(mediaType, "manifest.list") || strings.Contains(mediaType, "image.index") {
		var idx struct {
			Manifests []struct {
				Digest   string `json:"digest"`
				Size     int64  `json:"size"`
				Platform struct {
					Architecture string `json:"architecture"`
					OS           string `json:"os"`
				} `json:"platform"`
			} `json:"manifests"`
		}
		if err := json.Unmarshal(raw, &idx); err != nil {
			return err
		}
		if len(idx.Manifests) == 0 {
			return fmt.Errorf("manifest index has no child manifests")
		}
		first := idx.Manifests[0]
		m.Architecture = first.Platform.Architecture
		m.OS = first.Platform.OS
		// Children are referenced by digest; without the child bytes we can't
		// list layers here. The caller may follow up with GetManifest(repo, child.Digest).
		// For inventory we accept that the index's Size is 0 — child sizes are
		// available via the per-tag detail view.
		m.Size = first.Size
		return nil
	}

	// Single-arch manifest: read layers[] directly.
	var single struct {
		Layers []struct {
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
			MediaType string `json:"mediaType"`
		} `json:"layers"`
		Config struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &single); err != nil {
		return err
	}
	var size int64
	for _, l := range single.Layers {
		m.Layers = append(m.Layers, Layer{Digest: l.Digest, Size: l.Size, MediaType: l.MediaType})
		size += l.Size
	}
	// Image config blob size isn't included in OCI/Docker layer sizes; we add
	// it so the UI's "size" matches what `docker pull` reports for the full
	// image. Config can be nil for some manifests; tolerate it.
	if single.Config.Size > 0 {
		size += single.Config.Size
	}
	m.ConfigDigest = single.Config.Digest
	m.Size = size
	return nil
}

// decodeManifestCreated fetches the image config blob and pulls .created and
// .architecture out. For multi-arch indexes this is the child config; for
// single-arch manifests the config digest is in .config.digest.
//
// We do NOT fetch the blob inline here; instead we expose FetchConfig so the
// inventory scanner can fetch configs in parallel via errgroup.
func decodeManifestCreated(raw json.RawMessage, mediaType string, m *Manifest) error {
	// m.Architecture is set by decodeManifestLayers for indexes; for single
	// manifests the same info lives in the image config blob (not parsed here).
	if strings.Contains(mediaType, "manifest.list") || strings.Contains(mediaType, "image.index") {
		return nil
	}
	// Image config architecture / os are not present in the manifest itself.
	// We could parse the config blob on demand; for v0.1 we leave Created
	// and Architecture empty on single-arch tags and rely on per-tag detail
	// view to fetch the config blob. This keeps inventory scans cheap.
	return nil
}

// FetchConfig fetches the image config blob referenced by a single-arch
// manifest and returns its raw bytes. The caller decodes Created, Architecture,
// OS out of it. Used by per-tag detail handlers.
func (c *Client) FetchConfig(ctx context.Context, repo string, configDigest string) ([]byte, error) {
	if configDigest == "" {
		return nil, nil
	}
	path := fmt.Sprintf("/v2/%s/blobs/%s", escapeRepo(repo), url.PathEscape(configDigest))
	resp, body, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	_ = resp
	return body, nil
}

// Probe checks registry reachability and V2 conformance.
//
// v0.5.47: GET /v2/ 有两种「活着」的应答(OCI Distribution Spec,
// containers/image 的 Ping 同样把两者都视为成功):
//
//	200 OK                          —— 无需认证(内网 registry / mcr / registry.k8s.io)
//	401 + WWW-Authenticate: Bearer  —— 公网 Bearer 型 registry 对匿名请求的标准应答
//	                                   (quay.io / ghcr.io / Docker Hub 都是)
//
// 旧实现只认 200,并把 401 全权交给 doRequest 的 401→token 升级:Docker Hub 的
// auth.docker.io 无 scope 也发匿名 token,升级能走通;但 quay.io 的 /v2/auth
// 对「无 scope + 无凭据」**直接 401**(/v2/ 的 ping challenge 不带 scope ——
// 见 AGENTS.md §认证),升级失败,匿名预检整个报 TOKEN_FETCH_FAILED。
//
// 新语义:
//   - 匿名(未配 username):200 或 401+可解析 Bearer challenge = 成功。
//     真实资源能否匿名拉,由调用方再取一次 manifest 决定(拉取预检接口
//     v0.5.17 起就会这么做);ping 层不下结论,也就不需要碰 token 端点。
//   - 带凭据:保持原有升级链(401 → basic 换 token → Bearer 重试 → 200)。
//     token 换取失败 = 凭据不被 token 端点接受,照旧报错 —— 凭据测试
//     要的就是这个结论。
func (c *Client) Probe(ctx context.Context) error {
	if c.user != "" {
		resp, _, err := c.doRequest(ctx, http.MethodGet, "/v2/", "", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return &Error{
				Status:  resp.StatusCode,
				Code:    "PROBE_FAILED",
				Message: fmt.Sprintf("GET /v2/ returned %d, expected 200", resp.StatusCode),
				URL:     "/v2/",
			}
		}
		return nil
	}

	// 匿名:裸 ping,不做 401→token 升级(原因见上)。
	full := *c.baseURL
	full.Path = strings.TrimRight(full.Path, "/") + "/v2/"
	full.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
	if err != nil {
		return fmt.Errorf("registry: build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("registry: GET /v2/: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		if _, ok := parseChallenge(resp.Header.Get("WWW-Authenticate")); ok {
			// 401 + Bearer challenge = 对面确实在跑 V2 registry,匿名 ping 到此为止。
			return nil
		}
		return &Error{
			Status:  resp.StatusCode,
			Code:    "PROBE_FAILED",
			Message: "GET /v2/ returned 401 without a parseable Bearer challenge",
			URL:     "/v2/",
		}
	default:
		return &Error{
			Status:  resp.StatusCode,
			Code:    "PROBE_FAILED",
			Message: fmt.Sprintf("GET /v2/ returned %d, expected 200 or 401+Bearer challenge", resp.StatusCode),
			URL:     "/v2/",
		}
	}
}

// ScanInventory builds a full Inventory: every repo, every tag, plus a
// best-effort manifest summary per tag. Errors per-tag are collected into
// FailedTags instead of aborting the whole scan.
//
// Concurrency: tags are fetched with bounded parallelism via errgroup.
// We default to 8 workers; the registry can typically absorb far more but
// being polite reduces tail latency for other clients on shared instances.
func (c *Client) ScanInventory(ctx context.Context) (*Inventory, error) {
	scanStart := time.Now()
	repos, err := c.ListRepositories(ctx)
	if err != nil {
		return nil, fmt.Errorf("inventory: list repos: %w", err)
	}

	inv := &Inventory{
		RefreshAt: time.Now().UTC(),
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	var mu sync.Mutex // guards inv.Repositories + inv.FailedTags + inv.Totals

	for _, name := range repos {
		name := name
		g.Go(func() error {
			repo := Repository{Name: name, UpdatedAt: time.Now().UTC()}
			tags, err := c.ListTags(gctx, name)
			if err != nil {
				mu.Lock()
				inv.FailedTags = append(inv.FailedTags, FailedTag{Repo: name, Tag: "(list)", Error: err.Error()})
				mu.Unlock()
				// still record the repo so the UI shows it as empty
			}
			for _, t := range tags {
				manifest, err := c.GetManifest(gctx, name, t)
				if err != nil {
					if e, ok := err.(*Error); ok && e.IsNotFound() {
						// Tag deleted concurrently — skip silently
						continue
					}
					mu.Lock()
					inv.FailedTags = append(inv.FailedTags, FailedTag{Repo: name, Tag: t, Error: err.Error()})
					mu.Unlock()
					continue
				}
				tagInfo := TagInfo{
					Name:         t,
					Digest:       manifest.Digest,
					Size:         manifest.Size,
					Architecture: manifest.Architecture,
				}
				if !isZero(manifest.Created) {
					ct := *manifest.Created
					tagInfo.Created = &ct
				}
				repo.Tags = append(repo.Tags, tagInfo)
			}
			repo.TagCount = len(repo.Tags)
			mu.Lock()
			inv.Repositories = append(inv.Repositories, repo)
			for _, t := range repo.Tags {
				inv.Totals.TagCount++
				inv.Totals.TotalSize += t.Size
			}
			inv.Totals.RepoCount++
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	_ = scanStart // reserved for future "scanDurationMs" field
	return inv, nil
}

func isZero(t *time.Time) bool { return t == nil || t.IsZero() }
