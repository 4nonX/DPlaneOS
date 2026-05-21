package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"dplaned/internal/middleware"
	"dplaned/internal/security"
	"github.com/gorilla/mux"
)

// confirmRoute wraps a HandlerFunc so that it requires a valid X-Confirm-Token
// header. The token must have been issued by POST /api/confirm/issue for the
// same operation and target within the last 60 seconds, by the same user.
//
// Use after permRoute in the middleware chain:
//
//	permRoute("storage", "admin", confirmRoute("pool_destroy", jsonField("name"), handler))
//
// targetFn extracts the operation target from the incoming request.
// Use jsonField, muxVar, or constTarget depending on where the target lives.
func confirmRoute(operation string, targetFn func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Confirm-Token")
		if token == "" {
			writeConfirmJSON(w, http.StatusForbidden, map[string]interface{}{
				"error": "confirmation token required for this operation",
				"code":  "confirm_required",
			})
			return
		}

		user, ok := middleware.GetUserFromContext(r)
		if !ok || user == nil {
			writeConfirmJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"error": "unauthorized",
			})
			return
		}

		// Buffer the body so targetFn can read it and the actual handler still gets it.
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeConfirmJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error": "failed to read request body",
				})
				return
			}
			r.Body.Close()
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		target := targetFn(r)
		r.Body = io.NopCloser(bytes.NewReader(body))

		if !security.ConsumeConfirmToken(token, operation, target, user.ID) {
			writeConfirmJSON(w, http.StatusForbidden, map[string]interface{}{
				"error": "invalid, expired, or already-used confirmation token",
				"code":  "confirm_invalid",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// jsonField returns a targetFn that extracts a named string field from the JSON request body.
func jsonField(field string) func(*http.Request) string {
	return func(r *http.Request) string {
		var m map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			return ""
		}
		if v, ok := m[field]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
}

// muxVar returns a targetFn that reads a gorilla/mux path variable.
func muxVar(name string) func(*http.Request) string {
	return func(r *http.Request) string {
		return mux.Vars(r)[name]
	}
}

// constTarget returns a targetFn that always returns s regardless of the request.
func constTarget(s string) func(*http.Request) string {
	return func(*http.Request) string { return s }
}

func writeConfirmJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
