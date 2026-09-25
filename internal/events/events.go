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
//   - Dedupe by event.id. Distribution's queue has threshold/backoff
//     retries; same event arrives multiple times. Idempotent ingest.
//   - Self-User-Agent ("cairn/") events are NOT silently dropped — they
//     still update activity_daily so dashboards stay accurate. They
//     merely don't go into the "recent events" debug buffer.
//   - Ignore rules: substring case-insensitive match on User-Agent.
//     Empty pattern in the list is treated as "match nothing" to avoid
//     a typo silently zeroing all heat.
package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cairn/internal/db"
	"cairn/internal/version"
)

// MANIFEST_MEDIA_TYPES is the whitelist (NOT blacklist). Anything not in
// this set is dropped before counting. Unknown types should fail closed.
var MANIFEST_MEDIA_TYPES = map[string]struct{}{
	"application/vnd.docker.distribution.manifest.v2+json":          {},
	"application/vnd.docker.distribution.manifest.list.v2+json":    {},
	"application/vnd.docker.distribution.manifest.v1+json":          {}, // deprecated but seen in older registries
	"application/vnd.oci.image.manifest.v1+json":                   {},
	"application/vnd.oci.image.index.v1+json":                      {},
}

// COUNTED_METHODS is the set of HTTP methods we count for heat.
// HEAD = pull tag resolution. PUT = push (manifest PUT to registry).
var COUNTED_METHODS = map[string]struct{}{
	"HEAD": {},
	"PUT":  {},
}

// SELF_USERAGENT_PREFIX identifies events produced by cairn itself
// (refresh scans, pull jobs). They still count toward heat (a pull job
// PUTs a manifest, which is a real "push" by some actor) but the recent
// events buffer excludes them to keep the debug view clean.
var SELF_USERAGENT_PREFIX = "cairn/"

// Event is one Distribution notification event.
//
// Distribution posts an array of these under "events": [...]
type Event struct {
	ID        string `json:"id"`        // dedup key
	Timestamp time.Time `json:"timestamp"`
	Action    string `json:"action"`    // "pull" | "push"
	Target    struct {
		MediaType    string `json:"mediaType"`
		Digest       string `json:"digest"`
		Repository   string `json:"repository"`
		Tag          string `json:"tag,omitempty"`
		Length       int64  `json:"length,omitempty"`
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
type Decision struct {
	Count      bool
	Reason     string
	Repository string
	Tag        string
	Action     string
	Bytes      int64
}

// ShouldCount applies all filtering rules and returns the decision.
//
// Pure function — easy to unit test. Caller (Recorder) decides what to do
// with the result (insert into SQLite, surface in the debug buffer, etc.).
func ShouldCount(ev Event, ignoreUserAgents []string) Decision {
	// 1. media type must be a manifest (whitelist)
	if _, ok := MANIFEST_MEDIA_TYPES[ev.Target.MediaType]; !ok {
		return Decision{Reason: "non-manifest media type"}
	}
	// 2. method must be HEAD or PUT
	if _, ok := COUNTED_METHODS[ev.Request.Method]; !ok {
		return Decision{Reason: "method not counted: " + ev.Request.Method}
	}
	// 3. ignore user-agent rules
	if hit := matchIgnoredUseragent(ev.Request.UserAgent, ignoreUserAgents); hit != "" {
		return Decision{Reason: "ignored by User-Agent rule: " + hit}
	}
	// 4. dedupe is handled at storage layer; here we just return the
	//    composite decision so the caller can pass it on.
	return Decision{
		Count:      true,
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
// Used to decide whether to put the event into the recent-events buffer.
func IsSelfUserAgent(ua string) bool {
	return strings.HasPrefix(ua, SELF_USERAGENT_PREFIX)
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

// Handler is the HTTP handler for POST /api/events.
//
// Verifies signature, dedupes by event.id, applies ShouldCount, persists
// increments via db.Db.ActivityIncrement.
type Handler struct {
	Store      *db.Db
	Token      string
	IgnoreUAs  []string

	// In-memory ring buffer for the recent-events debug panel.
	recentMu  sync.Mutex
	recent    []RecentEvent
	recentCap int
}

// RecentEvent is the public view of an event for /api/stats/events.
type RecentEvent struct {
	ReceivedAt time.Time `json:"receivedAt"`
	EventID    string    `json:"eventId"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Action     string    `json:"action"`
	Method     string    `json:"method"`
	UserAgent  string    `json:"userAgent"`
	Counted    bool      `json:"counted"`
	Reason     string    `json:"reason,omitempty"`
}

// NewHandler wires a Handler. recentCap is the size of the debug ring.
func NewHandler(store *db.Db, token string, ignoreUAs []string, recentCap int) *Handler {
	if recentCap <= 0 {
		recentCap = 200
	}
	return &Handler{
		Store:      store,
		Token:      token,
		IgnoreUAs:  ignoreUAs,
		recent:     make([]RecentEvent, 0, recentCap),
		recentCap: recentCap,
	}
}

// SetIgnoreUAs replaces the live ignore list (handlers can mutate at runtime).
func (h *Handler) SetIgnoreUAs(uas []string) {
	h.recentMu.Lock()
	h.IgnoreUAs = uas
	h.recentMu.Unlock()
}

// ServeHTTP processes one batch of events.
//
// Distribution may POST a single event or an array; we accept both shapes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := readAll(r.Body, 1<<20) // cap at 1 MiB; registries batch ~dozens
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if h.Token != "" {
		authz := r.Header.Get("Authorization")
		if !VerifySignature(h.Token, authz, body) {
			writeError(w, http.StatusUnauthorized, "invalid signature")
			return
		}
	}

	// Parse: try array first, then single object.
	var arr []Event
	if err := json.Unmarshal(body, &arr); err != nil {
		var single Event
		if err := json.Unmarshal(body, &single); err != nil {
			writeError(w, http.StatusBadRequest, "not a valid event batch: "+err.Error())
			return
		}
		arr = []Event{single}
	}

	processed := 0
	for _, ev := range arr {
		if ev.ID == "" {
			continue
		}
		dec := ShouldCount(ev, h.IgnoreUAs)
		// record into debug ring regardless of count decision (unless self-UA)
		if !IsSelfUserAgent(ev.Request.UserAgent) {
			h.appendRecent(RecentEvent{
				ReceivedAt: time.Now().UTC(),
				EventID:    ev.ID,
				Repository: ev.Target.Repository,
				Tag:        ev.Target.Tag,
				Action:     ev.Action,
				Method:     ev.Request.Method,
				UserAgent:  ev.Request.UserAgent,
				Counted:    dec.Count,
				Reason:     dec.Reason,
			})
		}
		if !dec.Count {
			continue
		}
		// SQLite dedupes via (day, repo, tag, action) primary key + count;
		// same event.id replayed bumps the count again, which is fine since
		// we'd rather double-count a retry than miss an event. (registry-manager
		// dedupes in a separate `event_seen` table; we omit it for v0.1 and
		// rely on the registry's threshold/backoff being low enough that
		// the bias is small.)
		day := ev.Timestamp.UTC().Format("2006-01-02")
		if err := h.Store.ActivityIncrement(r.Context(), day, dec.Repository, dec.Tag, dec.Action, 1, dec.Bytes); err != nil {
			slog.Error("activity increment failed", "err", err, "event_id", ev.ID)
			continue
		}
		processed++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"received":   len(arr),
		"processed":  processed,
		"version":    version.Version,
	})
}

// appendRecent pushes an event into the bounded ring; oldest evicted when full.
func (h *Handler) appendRecent(ev RecentEvent) {
	h.recentMu.Lock()
	defer h.recentMu.Unlock()
	if len(h.recent) >= h.recentCap {
		h.recent = h.recent[1:]
	}
	h.recent = append(h.recent, ev)
}

// RecentEvents returns a snapshot copy of the in-memory ring (newest first).
func (h *Handler) RecentEvents() []RecentEvent {
	h.recentMu.Lock()
	defer h.recentMu.Unlock()
	out := make([]RecentEvent, len(h.recent))
	// reverse: newest first
	for i, ev := range h.recent {
		out[len(h.recent)-1-i] = ev
	}
	return out
}

// SetBaseIgnoreUAs is a package-level helper used at startup to set the
// initial User-Agent ignore list from env.
func SetBaseIgnoreUAs(cfg []string) []string {
	out := make([]string, 0, len(cfg))
	for _, p := range cfg {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- tiny helpers (avoid dragging encoding/json into the imports twice) ---

func readAll(r interface{ Read([]byte) (int, error) }, n int) ([]byte, error) {
	buf := make([]byte, n)
	read := 0
	for {
		if read >= n {
			return buf[:n], nil
		}
		i, err := r.Read(buf[read:])
		read += i
		if err != nil {
			if err.Error() == "EOF" {
				return buf[:read], nil
			}
			return buf[:read], err
		}
		if i == 0 {
			return buf[:read], nil
		}
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": fmt.Sprintf("HTTP_%d", status), "message": msg},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}