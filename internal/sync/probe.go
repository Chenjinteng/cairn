package sync

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProbeResult describes the outcome of a connectivity check against
// the destination registry. Returned to the UI by the "测试连接"
// button on the new-sync-task form so they can validate URL + creds
// without having to save the task and run it to discover a typo.
//
// All fields are zero-value meaningful; callers should always check
// Reachable first — if false, AuthStatus / HTTPStatus are unreliable.
type ProbeResult struct {
	// Reachable is true if the server returned ANY HTTP response.
	// False means TCP / DNS / TLS / timeout failure.
	Reachable bool `json:"reachable"`

	// AuthStatus classifies the auth posture the server is speaking:
	//   "ok"                 — 200, our creds (or no creds) accepted
	//   "no_auth_required"   — 200 with no Authorization header sent
	//   "required_but_missing"— 401 Basic challenge, we sent no creds
	//   "wrong_creds"        — 401 Basic challenge, our creds rejected
	//   "not_registry"       — 404 (the URL doesn't host /v2/)
	//   "unknown"            — any other non-2xx
	// UI maps to icon strings: ok→绿色, wrong_creds/required_but_missing→
	// 红色, not_registry→橙色, unknown→灰色.
	AuthStatus string `json:"authStatus"`

	// HTTPStatus is the raw HTTP status code we got back (0 if
	// Reachable is false — connection failed before any response).
	HTTPStatus int `json:"httpStatus"`

	// Message is a Chinese-text, human-readable one-liner suitable for
	// an antd <Alert>. Keep it short — UI shows it inline.
	Message string `json:"message"`
}

// ProbeConnection checks basic reachability and auth posture of the
// destination registry. Used by the UI's "测试连接" button.
//
// Behavior:
//   - 5s timeout on the whole probe (connect + TLS + request + read).
//   - Builds GET {baseURL}/v2/ with optional `Authorization: Basic ...`
//     (when username OR password is non-empty; mixed-pair is allowed
//     here unlike Validate, since the operator is mid-typing).
//   - Categorizes the response into AuthStatus + Message for the UI.
func ProbeConnection(ctx context.Context, baseURL, username, password string) ProbeResult {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ProbeResult{Message: fmt.Sprintf("URL 解析失败: %v", err)}
	}
	if u.Scheme == "" || u.Host == "" {
		return ProbeResult{Message: "URL 缺 scheme (http/https) 或 host"}
	}

	ping := *u
	ping.Path = strings.TrimRight(u.Path, "/") + "/v2/"
	ping.RawQuery = ""
	ping.Fragment = ""

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, ping.String(), nil)
	if err != nil {
		return ProbeResult{Message: fmt.Sprintf("构造请求失败: %v", err)}
	}
	if username != "" || password != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Authorization", "Basic "+cred)
	}
	req.Header.Set("User-Agent", "cairn-sync-probe/0.6.13")

	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return ProbeResult{Message: fmt.Sprintf("连接失败: %v", err)}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if username != "" || password != "" {
			return ProbeResult{
				Reachable: true, AuthStatus: "ok", HTTPStatus: 200,
				Message: "可访问,Basic 认证通过",
			}
		}
		return ProbeResult{
			Reachable: true, AuthStatus: "no_auth_required", HTTPStatus: 200,
			Message: "可访问,匿名访问通过 (对端没要求认证)",
		}
	case http.StatusUnauthorized:
		auth := resp.Header.Get("WWW-Authenticate")
		if strings.HasPrefix(auth, "Basic ") {
			if username == "" && password == "" {
				return ProbeResult{
					Reachable: true, AuthStatus: "required_but_missing", HTTPStatus: 401,
					Message: "对端要求 Basic 认证 (你留空了用户名密码)",
				}
			}
			return ProbeResult{
				Reachable: true, AuthStatus: "wrong_creds", HTTPStatus: 401,
				Message: "认证失败 (用户名或密码被对端拒绝)",
			}
		}
		return ProbeResult{
			Reachable: true, AuthStatus: "unknown", HTTPStatus: 401,
			Message: "401 但 WWW-Authenticate 不是 Basic,cairn 不支持这种对端",
		}
	case http.StatusNotFound:
		return ProbeResult{
			Reachable: true, AuthStatus: "not_registry", HTTPStatus: 404,
			Message: "/v2/ 端点不存在 (这个 URL 看起来不是 OCI registry)",
		}
	default:
		return ProbeResult{
			Reachable: true, AuthStatus: "unknown", HTTPStatus: resp.StatusCode,
			Message: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}
}