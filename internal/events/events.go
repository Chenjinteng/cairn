// Package events implements the webhook receiver for Distribution
// notifications and the heat-aggregation pipeline.
//
// Design (mirrors registry-manager/server/events.mjs):
//   - Only count events for manifest media types. Filtering by media type
//     (not content-type, not action alone) is the load-bearing filter:
//     a single 15-layer pull produces 15 blob events with content-type
//     "application/octet-stream" that would otherwise amplify heat ~15x.
//   - Only count HEAD / PUT methods. Docker's pull is "HEAD tag → GET
//     digest → GET blobs"; the HEAD carries the tag, the GET does not.
//     Counting GET would double the count.
//   - Self-User-Agent ("cairn/") READ events are folded into a single
//     counter (totals.self) and never enter the recent-events ring: a
//     rescan produces one event per tag and would evict everything else.
//     Self WRITES (PUT, counted) DO enter the ring — they change heat, so
//     they must stay auditable.
//   - Events hitting a UA ignore rule are folded into totals.ignored and
//     also stay out of the ring, so unclassified new clients aren't
//     evicted by already-handled noise.
//   - Ignore rules: substring case-insensitive match on User-Agent.
//     Empty pattern in the list is treated as "match nothing" to avoid
//     a typo silently zeroing all heat.
//
// v0.4.0 note: there is no event_seen dedup table (registry-manager has
// one); Distribution retries may double-count. The bias is accepted until
// persistence lands. Counters and the client aggregate are in-memory and
// reset on restart — the recent-events ring always was.
package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cairn/internal/db"
	"cairn/internal/version"
)

// MANIFEST_MEDIA_TYPES is the whitelist (NOT blacklist). Anything not in
// this set is dropped before counting. Unknown types should fail closed.
var MANIFEST_MEDIA_TYPES = map[string]struct{}{
	"application/vnd.docker.distribution.manifest.v2+json":      {},
	"application/vnd.docker.distribution.manifest.list.v2+json": {},
	"application/vnd.docker.distribution.manifest.v1+json":      {}, // deprecated but seen in older registries
	"application/vnd.oci.image.manifest.v1+json":                {},
	"application/vnd.oci.image.index.v1+json":                   {},
}

// COUNTED_METHODS is the set of HTTP methods we count for heat.
// HEAD = pull tag resolution. PUT = push (manifest PUT to registry).
var COUNTED_METHODS = map[string]struct{}{
	"HEAD": {},
	"PUT":  {},
}

// SELF_USERAGENT_PREFIX identifies events produced by cairn itself
// (refresh scans, pull jobs).
var SELF_USERAGENT_PREFIX = "cairn/"

// Event is one Distribution notification event.
//
// Distribution posts an array of these under "events": [...]
type Event struct {
	ID        string    `json:"id"` // dedup key
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"` // "pull" | "push"
	Target    struct {
		MediaType  string `json:"mediaType"`
		Digest     string `json:"digest"`
		Repository string `json:"repository"`
		Tag        string `json:"tag,omitempty"`
		Length     int64  `json:"length,omitempty"`
	} `json:"target"`
	Request struct {
		Host       string `json:"host"`
		Method     string `json:"method"`
		UserAgent  string `json:"useragent"`
		RemoteAddr string `json:"remoteaddr"`
	} `json:"request"`
	Actor struct {
		Name string `json:"name"`
	} `json:"actor"`
}

// Decision is the result of ShouldCount.
//
// Reason is a machine code (stable across versions, greppable from the
// UI): OK / NOT_MANIFEST / METHOD_<verb> / IGNORED_UA:<pattern>.
type Decision struct {
	Count      bool
	Ignored    bool // hit a UA ignore rule (folded into totals.ignored)
	Reason     string
	Repository string
	Tag        string
	Action     string
	Bytes      int64
}

// ShouldCount applies all filtering rules and returns the decision.
//
// Pure function — easy to unit test. Caller (Handler) decides what to do
// with the result (insert into SQLite, surface in the debug buffer, etc.).
func ShouldCount(ev Event, ignoreUserAgents []string) Decision {
	// 1. media type must be a manifest (whitelist)
	if _, ok := MANIFEST_MEDIA_TYPES[ev.Target.MediaType]; !ok {
		return Decision{Reason: "NOT_MANIFEST"}
	}
	// 2. method must be HEAD or PUT
	if _, ok := COUNTED_METHODS[ev.Request.Method]; !ok {
		return Decision{Reason: "METHOD_" + ev.Request.Method}
	}
	// 3. ignore user-agent rules
	if hit := matchIgnoredUseragent(ev.Request.UserAgent, ignoreUserAgents); hit != "" {
		return Decision{Ignored: true, Reason: "IGNORED_UA:" + hit}
	}
	return Decision{
		Count:      true,
		Reason:     "OK",
		Repository: ev.Target.Repository,
		Tag:        ev.Target.Tag,
		Action:     ev.Action,
		Bytes:      ev.Target.Length,
	}
}

// matchIgnoredUseragent returns the first pattern that case-insensitively
// matches a substring of ua. Empty patterns never match.
func matchIgnoredUseragent(ua string, patterns []string) string {
	lc := strings.ToLower(ua)
	for _, p := range patterns {
		needle := strings.ToLower(strings.TrimSpace(p))
		if needle == "" {
			continue
		}
		if strings.Contains(lc, needle) {
			return p
		}
	}
	return ""
}

// IsSelfUserAgent returns true if the User-Agent was emitted by this binary.
func IsSelfUserAgent(ua string) bool {
	return strings.HasPrefix(ua, SELF_USERAGENT_PREFIX)
}

// MergeIgnore unions env-provided and panel-provided UA rules, preserving
// order (env first) and dropping duplicates + blanks.
func MergeIgnore(lists ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, l := range lists {
		for _, p := range l {
			t := strings.TrimSpace(p)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// VerifySignature validates the Authorization header from a Distribution
// notifications request.
//
// Distribution sends `Authorization: <algo> <hex-digest>` (e.g.
// "sha256=abcdef..."). The expected digest is HMAC-SHA256 of the body with
// the shared token. We use hmac.Equal for constant-time comparison.
func VerifySignature(token string, header string, body []byte) bool {
	if token == "" {
		return false
	}
	header = strings.TrimSpace(header)
	eq := strings.IndexByte(header, '=')
	if eq < 0 {
		return false
	}
	algo := strings.ToLower(strings.TrimSpace(header[:eq]))
	digestHex := strings.TrimSpace(header[eq+1:])
	if algo != "sha256" {
		return false
	}
	want, err := hex.DecodeString(digestHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(body)
	got := mac.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Handler is the HTTP handler for the notification webhook.
//
// Verifies signature, applies ShouldCount, persists increments via
// db.Db.ActivityIncrement, and maintains three in-memory views:
//   - recent ring (per-event debug panel, /api/stats/events items)
//   - lifetime counters (totals: accepted/rejected/self/ignored)
//   - per-client aggregate (/api/stats/clients)
type Handler struct {
	Store *db.Db
	Token string

	// enabled (v0.5.4) is an optional live predicate consulted on every
	// request. nil means "always enabled". server.go installs
	// cfg.AllowRegistryEvents so flipping allow.registry_events on
	// the settings page starts/stops ingestion without a restart.
	enabled func() bool

	mu        sync.Mutex
	ignoreUAs []string
	recent    []RecentEvent
	recentCap int
	clients   map[string]*clientAgg

	accepted    atomic.Int64 // counted → heat + ring
	rejected    atomic.Int64 // not counted, not ignored, not self-read → ring
	selfFolded  atomic.Int64 // self UA reads, folded (no ring)
	ignoredFold atomic.Int64 // hit ignore rule, folded (no ring)
}

// RecentEvent is the public view of an event; its JSON shape is exactly
// the UI's StatsEventItem.
type RecentEvent struct {
	At         time.Time `json:"at"`      // server receive time
	EventAt    time.Time `json:"eventAt"` // registry-side timestamp
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Method     string    `json:"method"`
	MediaType  string    `json:"mediaType"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	UserAgent  string    `json:"useragent"`
	Addr       string    `json:"addr"`
	Host       string    `json:"host"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"` // OK when counted
	Counted    bool      `json:"counted"`
}

// clientAgg accumulates per-User-Agent totals (in-memory; resets on
// restart). Row count is bounded by distinct UAs, so it stays small even
// for slow-dripping clients the ring buffer would have evicted.
type clientAgg struct {
	UserAgent   string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Events      int64
	Counted     int64
	Self        bool
}

// ClientStat is the public per-client view (/api/stats/clients items).
type ClientStat struct {
	UserAgent   string    `json:"useragent"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	Events      int64     `json:"events"`
	Counted     int64     `json:"counted"`
	Self        bool      `json:"self"`
}

// Totals is the counters block of /api/stats/events.
type Totals struct {
	Accepted   int64 `json:"accepted"`
	Rejected   int64 `json:"rejected"`
	Buffered   int   `json:"buffered"`
	BufferSize int   `json:"bufferSize"`
	Self       int64 `json:"self"`
	Ignored    int64 `json:"ignored"`
}

// NewHandler wires a Handler. recentCap is the size of the debug ring.
func NewHandler(store *db.Db, token string, ignoreUAs []string, recentCap int) *Handler {
	if recentCap <= 0 {
		recentCap = 200
	}
	return &Handler{
		Store:     store,
		Token:     token,
		ignoreUAs: append([]string{}, ignoreUAs...),
		recent:    make([]RecentEvent, 0, recentCap),
		recentCap: recentCap,
		clients:   map[string]*clientAgg{},
	}
}

// SetIgnoreUAs replaces the live (effective = env ∪ panel) ignore list.
func (h *Handler) SetIgnoreUAs(uas []string) {
	h.mu.Lock()
	h.ignoreUAs = append([]string{}, uas...)
	h.mu.Unlock()
}

// SetEnabled installs a live on/off predicate (v0.5.4). Passing nil
// restores "always enabled". The predicate is invoked OUTSIDE h.mu so it
// may safely take locks of its own (config.Mutable does).
func (h *Handler) SetEnabled(fn func() bool) {
	h.mu.Lock()
	h.enabled = fn
	h.mu.Unlock()
}

// isEnabled reports whether webhook ingestion is switched on right now.
func (h *Handler) isEnabled() bool {
	h.mu.Lock()
	fn := h.enabled
	h.mu.Unlock()
	return fn == nil || fn()
}

// currentIgnore returns a copy of the live ignore list.
func (h *Handler) currentIgnore() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.ignoreUAs...)
}

// ServeHTTP processes one batch of notification events.
//
// Distribution may POST a single event or an array; we accept both shapes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// v0.5.4: honour allow.registry_events at request time. Before this the
	// handler was simply not constructed when the env flag was false, so a
	// runtime override could never switch ingestion back on (and switching
	// it off needed a restart).
	if !h.isEnabled() {
		h.writeErr(w, http.StatusForbidden, "registry events are disabled (allow.registry_events=false)")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // cap at 1 MiB
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if !VerifySignature(h.Token, r.Header.Get("Authorization"), body) {
		h.writeErr(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// Parse: try array first, then single object.
	var arr []Event
	if err := json.Unmarshal(body, &arr); err != nil {
		var single Event
		if err := json.Unmarshal(body, &single); err != nil {
			h.writeErr(w, http.StatusBadRequest, "not a valid event batch: "+err.Error())
			return
		}
		arr = []Event{single}
	}

	ignore := h.currentIgnore()
	now := time.Now().UTC()
	processed := 0
	for _, ev := range arr {
		if ev.ID == "" {
			continue
		}
		if h.processOne(r.Context(), ev, now, ignore) {
			processed++
		}
	}
	h.writeOK(w, http.StatusAccepted, map[string]any{
		"received":  len(arr),
		"processed": processed,
		"version":   version.Version,
	})
}

// recordClient updates the per-UA aggregate. Every received event counts
// toward Events (including ignored ones — "events > 0 but counted == 0"
// is how the UI shows "seen but filtered").
func (h *Handler) recordClient(ua string, now time.Time, counted, self bool) {
	if ua == "" {
		ua = "(empty)"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[ua]
	if !ok {
		c = &clientAgg{UserAgent: ua, FirstSeenAt: now, Self: self}
		h.clients[ua] = c
	}
	c.LastSeenAt = now
	c.Events++
	if counted {
		c.Counted++
	}
}

// appendRecent pushes an event into the bounded ring; oldest evicted when full.
func (h *Handler) appendRecent(ev RecentEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.recent) >= h.recentCap {
		h.recent = h.recent[1:]
	}
	h.recent = append(h.recent, ev)
}

// RecentEvents returns a snapshot copy of the in-memory ring (newest first).
func (h *Handler) RecentEvents() []RecentEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RecentEvent, len(h.recent))
	for i, ev := range h.recent {
		out[len(h.recent)-1-i] = ev
	}
	return out
}

// processOne runs the per-event pipeline (ShouldCount → recordClient →
// ring → ActivityIncrement). Returns true iff the event was counted
// (heat + ring). Extracted from ServeHTTP in v0.5.8 so registryd can
// push in-process events through the exact same code path as the
// webhook (no duplicated filter rules).
//
// ctx is the request context from the webhook call (or
// context.Background from IngestLocal). ignore is a snapshot of the
// live ignore list taken once at batch start — processOne must NOT
// re-read h.currentIgnore() so a UA toggle mid-batch behaves the same
// as in v0.4.x..v0.5.7.
func (h *Handler) processOne(ctx context.Context, ev Event, now time.Time, ignore []string) bool {
	dec := ShouldCount(ev, ignore)
	self := IsSelfUserAgent(ev.Request.UserAgent)

	h.recordClient(ev.Request.UserAgent, now, dec.Count, self)

	recent := RecentEvent{
		At:         now,
		EventAt:    ev.Timestamp.UTC(),
		ID:         ev.ID,
		Action:     ev.Action,
		Method:     ev.Request.Method,
		MediaType:  ev.Target.MediaType,
		Repository: ev.Target.Repository,
		Tag:        ev.Target.Tag,
		UserAgent:  ev.Request.UserAgent,
		Addr:       ev.Request.RemoteAddr,
		Host:       ev.Request.Host,
		Actor:      ev.Actor.Name,
		Reason:     dec.Reason,
		Counted:    dec.Count,
	}

	switch {
	case dec.Count:
		// Counted events always enter the ring — including self PUTs,
		// so "why did heat change" stays answerable.
		h.accepted.Add(1)
		h.appendRecent(recent)
	case dec.Ignored:
		h.ignoredFold.Add(1) // folded; keeps the ring free for new clients
	case self:
		h.selfFolded.Add(1) // self reads (rescans): pure noise, folded
	default:
		h.rejected.Add(1)
		h.appendRecent(recent)
	}

	if !dec.Count {
		return false
	}
	// No event_seen dedup in v0.4.0: a replayed event.id bumps the
	// count again. Accepted bias (see package doc).
	day := ev.Timestamp.UTC().Format("2006-01-02")
	if h.Store != nil {
		if err := h.Store.ActivityIncrement(ctx, day, dec.Repository, dec.Tag, dec.Action, 1, dec.Bytes); err != nil {
			slog.Error("activity increment failed", "err", err, "event_id", ev.ID)
			return false
		}
	}
	return true
}

// SnapshotTotals returns the lifetime counters plus current ring occupancy.
func (h *Handler) SnapshotTotals() Totals {
	h.mu.Lock()
	buffered := len(h.recent)
	size := h.recentCap
	h.mu.Unlock()
	return Totals{
		Accepted:   h.accepted.Load(),
		Rejected:   h.rejected.Load(),
		Buffered:   buffered,
		BufferSize: size,
		Self:       h.selfFolded.Load(),
		Ignored:    h.ignoredFold.Load(),
	}
}

// SnapshotClients returns the per-UA aggregate, most recently seen first.
func (h *Handler) SnapshotClients() []ClientStat {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ClientStat, 0, len(h.clients))
	for _, c := range h.clients {
		out = append(out, ClientStat{
			UserAgent:   c.UserAgent,
			FirstSeenAt: c.FirstSeenAt,
			LastSeenAt:  c.LastSeenAt,
			Events:      c.Events,
			Counted:     c.Counted,
			Self:        c.Self,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out
}

// SetBaseIgnoreUAs normalizes the env-provided ignore list at startup.
func SetBaseIgnoreUAs(cfg []string) []string {
	out := make([]string, 0, len(cfg))
	for _, p := range cfg {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- tiny helpers: the webhook speaks plain JSON to the registry, NOT the
// {success,code,message,data} envelope the UI API uses. Keep them private
// so nobody mistakes them for the api-package writers. ---

func (h *Handler) writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": fmt.Sprintf("HTTP_%d", status), "message": msg},
	})
}

func (h *Handler) writeOK(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// IngestLocal feeds an in-process event (built by registryd for an
// observed /v2/ operation) into the same pipeline as the webhook.
// Returns true iff the event was counted (heat + ring).
//
// v0.5.8: registryd is the source of truth for what the user did
// against the built-in registry. Pushing through processOne instead
// of recording directly keeps the ShouldCount filter, the self-fold
// behaviour, the ignore-rule folding and the recent-events ring
// unified across built-in and external registries.
//
// Safe to call on a nil handler (no-op returns false); safe to call
// when allow.registry_events=false (kill switch honoured at ingest
// time, not just at handler-construction time). Safe on a webhook
// event with the same shape (used by tests).
//
// ev.ID and ev.Timestamp are auto-filled if empty — the built-in
// registry doesn't observe Distribution's request ID, so we synthesise
// one. UA, method, media type, actor are caller responsibility.
func (h *Handler) IngestLocal(ev Event) bool {
	if h == nil {
		return false
	}
	if !h.isEnabled() {
		return false
	}
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("local-%d", time.Now().UTC().UnixNano())
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	return h.processOne(context.Background(), ev, time.Now().UTC(), h.currentIgnore())
}
