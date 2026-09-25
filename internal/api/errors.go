// Package api wires HTTP handlers onto a chi router.
//
// Error responses mirror registry-manager's shape so the React frontend works
// without changes:
//
//	{ "error": { "code": "...", "message": "...", "detail": "..." } }
//
// All handlers return JSON for both success and error; never HTML.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"cairn/internal/registry"
)

// ErrorBody is the JSON shape used for every non-2xx response.
// "code" is machine-readable; "message" is human-readable; "detail" is
// optional context (e.g. the offending digest).
type ErrorBody struct {
	Error ErrorPayload `json:"error"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// writeError emits an ErrorBody wrapped in the {success:false, code, message}
// shape the frontend expects. If the error is a *registry.Error, its code
// + message are surfaced; otherwise the generic INTERNAL_ERROR code is used.
func writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	body := ErrorBody{Error: ErrorPayload{
		Code:    codeForStatus(status),
		Message: http.StatusText(status),
	}}
	if err != nil {
		body.Error.Message = err.Error()
		if re := (*registry.Error)(nil); errors.As(err, &re) {
			if re.Code != "" {
				body.Error.Code = re.Code
			}
			body.Error.Detail = re.URL
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Wrap in the ApiResult shape the frontend parses:
	// { success: false, code, message, error: {code, message, detail} }
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"code":    body.Error.Code,
		"message": body.Error.Message,
		"error":   body.Error,
	})

	if status >= 500 {
		slog.ErrorContext(r.Context(), "api error", "status", status, "code", body.Error.Code, "err", err)
	} else {
		slog.DebugContext(r.Context(), "api error", "status", status, "code", body.Error.Code, "err", err)
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusGone:
		return "GONE"
	default:
		if status >= 500 {
			return "INTERNAL_ERROR"
		}
		return "ERROR"
	}
}

// writeJSON wraps body in the {success, code, message, data} envelope the
// frontend's request<T>() helper expects. body becomes response.data; the
// spread at top level is dropped because the frontend accesses .data, not
// the bare fields.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"code":    "OK",
		"message": "",
		"data":    body,
	})
}