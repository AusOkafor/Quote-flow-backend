package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
)

type contextKey string

const UserContextKey contextKey = "user"

// JWTVerifier verifies Supabase JWTs using the project's JWKS (ES256).
type JWTVerifier struct {
	cache    *jwk.Cache
	jwksURL  string
}

// NewJWTVerifier creates a verifier that fetches and caches the Supabase JWKS.
func NewJWTVerifier(cfg *config.Config) (*JWTVerifier, error) {
	jwksURL := strings.TrimSuffix(cfg.SupabaseURL, "/") + "/auth/v1/.well-known/jwks.json"

	// Whitelist only our Supabase JWKS URL
	whitelist := jwk.NewMapWhitelist().Add(jwksURL)

	cache := jwk.NewCache(context.Background())
	if err := cache.Register(jwksURL, jwk.WithFetchWhitelist(whitelist), jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return nil, err
	}

	// Pre-fetch so we fail fast if the URL is unreachable
	if _, err := cache.Refresh(context.Background(), jwksURL); err != nil {
		return nil, err
	}

	return &JWTVerifier{cache: cache, jwksURL: jwksURL}, nil
}

// RequireAuth verifies the JWT using JWKS and injects *models.User into context.
func RequireAuth(v *JWTVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			if tokenStr == "" {
				jsonErr(w, http.StatusUnauthorized, "missing authentication token")
				return
			}

			keySet, err := v.cache.Get(r.Context(), v.jwksURL)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "failed to get signing keys")
				return
			}

			token, err := jwt.Parse([]byte(tokenStr), jwt.WithKeySet(keySet))
			if err != nil {
				jsonErr(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			if token.Expiration().Before(time.Now()) {
				jsonErr(w, http.StatusUnauthorized, "token has expired")
				return
			}

			userID := token.Subject()
			email, _ := token.Get("email")
			emailStr, _ := email.(string)

			if userID == "" {
				jsonErr(w, http.StatusUnauthorized, "invalid token: missing sub claim")
				return
			}

			ctx := context.WithValue(r.Context(), UserContextKey, &models.User{
				ID:    userID,
				Email: emailStr,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext retrieves the authenticated user from request context.
func UserFromContext(ctx context.Context) (*models.User, bool) {
	user, ok := ctx.Value(UserContextKey).(*models.User)
	return user, ok && user != nil
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("qf_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"success":false,"error":"` + msg + `"}`))
}
