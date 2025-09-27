package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mylxsw/asteria/log"
)

type APIKeyAuth struct {
	keys map[string]struct{}
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewAPIKeyAuth(keys []string) *APIKeyAuth {
	m := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		m[key] = struct{}{}
	}
	return &APIKeyAuth{keys: m}
}

func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return a.MiddlewareWithSkipper(nil)(next)
}

func (a *APIKeyAuth) MiddlewareWithSkipper(skipper func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(a.keys) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if skipper != nil && skipper(r) {
				next.ServeHTTP(w, r)
				return
			}

			key := extractAPIKey(r)
			if key == "" {
				log.Warningf("Missing API key from %s", r.RemoteAddr)
				writeAuthError(w, http.StatusUnauthorized, "missing api key")
				return
			}
			if _, ok := a.keys[key]; !ok {
				log.Warningf("Invalid API key from %s", r.RemoteAddr)
				writeAuthError(w, http.StatusUnauthorized, "invalid api key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractAPIKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth != "" {
		fields := strings.Fields(auth)
		if len(fields) == 2 && strings.ToLower(fields[0]) == "bearer" {
			return fields[1]
		}
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}
