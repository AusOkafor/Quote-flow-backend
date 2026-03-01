package middleware

import (
	"net/http"
	"strings"

	"quoteflow-backend/config"
)

// RequireCronSecret ensures the request is authorized with CRON_SECRET.
// Expects: Authorization: Bearer <CRON_SECRET>
func RequireCronSecret(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.CronSecret == "" {
				http.Error(w, `{"success":false,"error":"cron not configured"}`, http.StatusServiceUnavailable)
				return
			}
			token := ""
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			}
			if token != cfg.CronSecret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"success":false,"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
