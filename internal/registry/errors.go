package registry

import "fmt"

// Error is a typed error from the registry client.
//
// We map V2 errors onto HTTP-style codes (MANIFEST_UNKNOWN, DIGEST_INVALID...)
// because the registry uses them in problem documents and callers (especially
// the UI) want to distinguish "tag missing" from "auth required" without
// parsing free-text messages.
type Error struct {
	Status  int    // HTTP status from the registry
	Code    string // V2 error code (e.g. MANIFEST_UNKNOWN), empty if not parseable
	Message string // human-readable message from the registry's problem document
	URL     string // request URL that produced the error
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("registry %s: %s (status=%d url=%s)", e.Code, e.Message, e.Status, e.URL)
	}
	return fmt.Sprintf("registry error: %s (status=%d url=%s)", e.Message, e.Status, e.URL)
}

// IsNotFound reports whether the error indicates a missing repository/tag/manifest.
// Callers use it to swallow 404s gracefully during inventory scanning.
func (e *Error) IsNotFound() bool {
	if e.Status == 404 {
		return true
	}
	switch e.Code {
	case "MANIFEST_UNKNOWN", "BLOB_UNKNOWN", "NAME_UNKNOWN", "TAG_INVALID":
		// TAG_INVALID can also mean "manifest is a random tag, not a digest"
		// (registry returns 400 with this code for invalid references).
		return true
	}
	return false
}