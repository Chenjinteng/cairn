package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/events"
	"cairn/internal/proxies"
	"cairn/internal/pull"
	"cairn/internal/registry"
)

// ExtraHandlers bundles deps that aren't in Handlers yet (so the v0.1
// handlers.go stays small and reviewable).
type ExtraHandlers struct {
	Cfg      *ConfigExtras
	Executor *pull.Executor
	Vault    *credentials.Vault
	Proxies  *proxies.Store
	DB       *db.Db
	Events   *events.Handler
	Registry registry.Registry
}

// ConfigExtras holds the extra config fields the new handlers need.
type ConfigExtras struct {
	AllowPull       bool
	IgnoreUserAgents []string
}

// RegisterExtraRoutes mounts the v0.2 + v0.3 endpoints on the chi router.
//
// Pattern: separate from the v0.1 routes so we can group them in the README
// and let middleware differ (e.g. /api/pull/* could rate-limit later).
func (e *ExtraHandlers) RegisterRoutes(r chi.Router) {
	r.Route("/api/pull", func(r chi.Router) {
		r.Get("/jobs", e.ListPullJobs)
		r.Post("/jobs", e.CreatePullJob)
		r.Get("/jobs/{id}", e.GetPullJob)
		r.Post("/jobs/{id}/cancel", e.CancelPullJob)
		r.Delete("/jobs/{id}", e.DeletePullJob)
		r.Post("/probe", e.ProbePullSource)
	})

	r.Route("/api/credentials", func(r chi.Router) {
		r.Get("/", e.ListCredentials)
		r.Post("/", e.CreateCredential)
		r.Get("/{id}", e.GetCredential)
		r.Delete("/{id}", e.DeleteCredential)
		r.Post("/{id}/test", e.TestCredential)
	})

	r.Route("/api/proxies", func(r chi.Router) {
		r.Get("/", e.ListProxies)
		r.Post("/", e.CreateProxy)
		r.Get("/{id}", e.GetProxy)
		r.Delete("/{id}", e.DeleteProxy)
		r.Post("/{id}/test", e.TestProxy)
	})

	if e.Events != nil {
		r.Post("/events", e.Events.ServeHTTP)
	}
	r.Route("/api/stats", func(r chi.Router) {
		r.Get("/summary", e.StatsSummary)
		r.Get("/top", e.StatsTop)
		r.Get("/series", e.StatsSeries)
		r.Get("/repositories", e.StatsRepositories)
		r.Get("/events", e.StatsEvents)
		r.Get("/clients", e.StatsClients)
		r.Get("/ignore", e.StatsIgnoreGet)
		r.Post("/ignore", e.StatsIgnoreAdd)
		r.Delete("/ignore", e.StatsIgnoreRemove)
		r.Delete("/heat", e.StatsHeatDelete)
	})
}

// --- Pull -------------------------------------------------------------------

type CreatePullJobReq struct {
	SourceRef  string `json:"sourceRef"`
	DestRepo   string `json:"destRepo"`
	DestTag    string `json:"destTag"`
	Credential string `json:"credential,omitempty"`
	Proxy      string `json:"proxy,omitempty"`
}

func (e *ExtraHandlers) CreatePullJob(w http.ResponseWriter, r *http.Request) {
	if !e.Cfg.AllowPull {
		writeError(w, r, http.StatusForbidden, errDeleteDisabled) // reuse 403 path
		return
	}
	var req CreatePullJobReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.SourceRef == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("sourceRef required"))
		return
	}
	if _, _, ok := splitSourceRef(req.SourceRef); !ok {
		writeError(w, r, http.StatusBadRequest, errors.New("sourceRef must be <repo>:<tag>"))
		return
	}
	job := e.Executor.Submit(req.SourceRef, req.DestRepo, req.DestTag, req.Credential, req.Proxy)
	writeJSON(w, http.StatusAccepted, job)
}

func (e *ExtraHandlers) ListPullJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, e.Executor.List())
}

func (e *ExtraHandlers) GetPullJob(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if j := e.Executor.Get(id); j.ID != "" {
		writeJSON(w, http.StatusOK, j)
		return
	}
	writeError(w, r, http.StatusNotFound, errors.New("job not found"))
}

func (e *ExtraHandlers) CancelPullJob(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if err := e.Executor.Cancel(id); err != nil {
		if errors.Is(err, pull.ErrJobNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "cancelled": true})
}

func (e *ExtraHandlers) DeletePullJob(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if err := e.Executor.Delete(id); err != nil {
		if errors.Is(err, pull.ErrJobNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, pull.ErrJobNotTerminal) {
			writeError(w, r, http.StatusConflict, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *ExtraHandlers) ProbePullSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL        string `json:"url"`
		Credential string `json:"credential,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.URL == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("url required"))
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeError(w, r, http.StatusBadRequest, errors.New("url must be http(s)://..."))
		return
	}
	cfg := registry.Config{BaseURL: req.URL, Timeout: 5 * time.Second}
	if req.Credential != "" {
		c, err := e.Vault.Get(req.Credential)
		if err != nil {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		cfg.Username = c.Username
		cfg.Password = c.Password
	}
	client, err := registry.NewClient(cfg)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := client.Probe(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func splitSourceRef(ref string) (repo, tag string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return "", "", false
	}
	repo = strings.TrimLeft(ref[:i], "/")
	tag = ref[i+1:]
	if repo == "" || tag == "" {
		return "", "", false
	}
	return repo, tag, true
}

// --- Credentials ------------------------------------------------------------

func (e *ExtraHandlers) ListCredentials(w http.ResponseWriter, r *http.Request) {
	out := []credentials.Credential{}
	for _, c := range e.Vault.List() {
		c.Password = "" // never echo password
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) CreateCredential(w http.ResponseWriter, r *http.Request) {
	var c credentials.Credential
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if c.ID == "" {
		c.ID = newID()
	}
	if err := e.Vault.Put(c); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Vault.Get(c.ID)
	stored.Password = "" // don't echo back
	writeJSON(w, http.StatusCreated, stored)
}

func (e *ExtraHandlers) GetCredential(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	c, err := e.Vault.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	c.Password = ""
	writeJSON(w, http.StatusOK, c)
}

func (e *ExtraHandlers) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if err := e.Vault.Delete(id); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *ExtraHandlers) TestCredential(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	c, err := e.Vault.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	client, err := registry.NewClient(registry.Config{
		BaseURL:  c.URL,
		Username: c.Username,
		Password: c.Password,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := client.Probe(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Proxies ----------------------------------------------------------------

func (e *ExtraHandlers) ListProxies(w http.ResponseWriter, r *http.Request) {
	out := []proxies.Proxy{}
	for _, p := range e.Proxies.List() {
		p.Password = ""
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) CreateProxy(w http.ResponseWriter, r *http.Request) {
	var p proxies.Proxy
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if p.ID == "" {
		p.ID = newID()
	}
	if err := e.Proxies.Put(p); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Proxies.Get(p.ID)
	stored.Password = ""
	writeJSON(w, http.StatusCreated, stored)
}

func (e *ExtraHandlers) GetProxy(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	p, err := e.Proxies.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	p.Password = ""
	writeJSON(w, http.StatusOK, p)
}

func (e *ExtraHandlers) DeleteProxy(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if err := e.Proxies.Delete(id); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *ExtraHandlers) TestProxy(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	p, err := e.Proxies.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	client, err := registry.NewClient(registry.Config{
		BaseURL: "https://example.com", // dummy; we only care that the proxy connects
		Proxy:   p.URL,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// attempt a HEAD on the proxy itself (CONNECT-style) — we'll do a GET
	// against a known-stable endpoint as a smoke test.
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "https://example.com/", nil)
	req.Header.Set("User-Agent", "cairn/probe")
	resp, err := client.HTTP().Do(req)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	resp.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"proxy":  p.URL,
		"test":   resp.StatusCode,
	})
}

// --- Stats ------------------------------------------------------------------

func (e *ExtraHandlers) StatsSummary(w http.ResponseWriter, r *http.Request) {
	since := parseSince(r, 30)
	s, err := e.DB.GetSummary(r.Context(), since)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (e *ExtraHandlers) StatsTop(w http.ResponseWriter, r *http.Request) {
	since := parseSince(r, 30)
	limit := parseLimit(r, 20)
	top, err := e.DB.GetTopRepos(r.Context(), since, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, top)
}

func (e *ExtraHandlers) StatsSeries(w http.ResponseWriter, r *http.Request) {
	since := parseSince(r, 90)
	limit := parseLimit(r, 5000)
	pts, err := e.DB.GetSeries(r.Context(), since, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

func (e *ExtraHandlers) StatsRepositories(w http.ResponseWriter, r *http.Request) {
	e.StatsTop(w, r)
}

func (e *ExtraHandlers) StatsEvents(w http.ResponseWriter, r *http.Request) {
	if e.Events == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, e.Events.RecentEvents())
}

func (e *ExtraHandlers) StatsClients(w http.ResponseWriter, r *http.Request) {
	// stub: aggregate User-Agent counts from the recent buffer
	if e.Events == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	type clientRow struct {
		UserAgent string `json:"userAgent"`
		Pulls     int    `json:"pulls"`
		Pushes    int    `json:"pushes"`
	}
	counts := map[string]*clientRow{}
	for _, ev := range e.Events.RecentEvents() {
		c, ok := counts[ev.UserAgent]
		if !ok {
			c = &clientRow{UserAgent: ev.UserAgent}
			counts[ev.UserAgent] = c
		}
		if !ev.Counted {
			continue
		}
		switch ev.Action {
		case "pull":
			c.Pulls++
		case "push":
			c.Pushes++
		}
	}
	out := make([]clientRow, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) StatsIgnoreGet(w http.ResponseWriter, r *http.Request) {
	if e.Events == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, e.Cfg.IgnoreUserAgents)
}

func (e *ExtraHandlers) StatsIgnoreAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pattern string `json:"pattern"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("pattern required"))
		return
	}
	for _, p := range e.Cfg.IgnoreUserAgents {
		if p == pattern {
			writeJSON(w, http.StatusOK, e.Cfg.IgnoreUserAgents)
			return
		}
	}
	e.Cfg.IgnoreUserAgents = append(e.Cfg.IgnoreUserAgents, pattern)
	if e.Events != nil {
		e.Events.SetIgnoreUAs(e.Cfg.IgnoreUserAgents)
	}
	writeJSON(w, http.StatusOK, e.Cfg.IgnoreUserAgents)
}

func (e *ExtraHandlers) StatsIgnoreRemove(w http.ResponseWriter, r *http.Request) {
	pattern := chiURLParam(r, "pattern")
	pattern = strings.TrimSpace(pattern)
	out := e.Cfg.IgnoreUserAgents[:0]
	for _, p := range e.Cfg.IgnoreUserAgents {
		if p != pattern {
			out = append(out, p)
		}
	}
	e.Cfg.IgnoreUserAgents = out
	if e.Events != nil {
		e.Events.SetIgnoreUAs(out)
	}
	writeJSON(w, http.StatusOK, e.Cfg.IgnoreUserAgents)
}

func (e *ExtraHandlers) StatsHeatDelete(w http.ResponseWriter, r *http.Request) {
	cutoff := time.Now().UTC().AddDate(0, 0, -365) // default: delete everything older than 1y
	if q := r.URL.Query().Get("olderThanDays"); q != "" {
		if d, err := time.ParseDuration(q + "h"); err == nil {
			cutoff = time.Now().UTC().Add(-d)
		}
	}
	n, err := e.DB.RetentionCleanup(r.Context(), cutoff)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// --- helpers ----------------------------------------------------------------

func parseSince(r *http.Request, defDays int) time.Time {
	if q := r.URL.Query().Get("days"); q != "" {
		if d, err := time.ParseDuration(q + "h"); err == nil {
			return time.Now().UTC().Add(-d)
		}
	}
	return time.Now().UTC().AddDate(0, 0, -defDays)
}

func parseLimit(r *http.Request, def int) int {
	if q := r.URL.Query().Get("limit"); q != "" {
		var n int
		if _, err := fmtSscan(q, &n); err == nil {
			return n
		}
	}
	return def
}

func newID() string {
	return strings.ReplaceAll(time.Now().UTC().Format("20060102-150405.000"), ".", "-") + "-" + randHex(4)
}

func randHex(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hex[time.Now().UnixNano()%16]
		time.Sleep(time.Microsecond) // cheap randomness
	}
	return string(b)
}

func fmtSscan(s string, n *int) (int, error) {
	v := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		v = v*10 + int(r-'0')
	}
	*n = v
	return len(s), nil
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// url.PathEscape re-exported (avoids an extra import in tests).
var _ = url.PathEscape