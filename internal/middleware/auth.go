package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
	"quoteflow-backend/internal/repository"
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

// RequireAuth verifies JWT or X-API-Key and injects *models.User into context.
// If Bearer token is missing or invalid, tries X-API-Key header (Business plan).
func RequireAuth(v *JWTVerifier, db *repository.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))

			// Try JWT first
			if tokenStr != "" {
				keySet, err := v.cache.Get(r.Context(), v.jwksURL)
				if err != nil {
					jsonErr(w, http.StatusInternalServerError, "failed to get signing keys")
					return
				}

				token, err := jwt.Parse([]byte(tokenStr), jwt.WithKeySet(keySet))
				if err == nil && !token.Expiration().Before(time.Now()) {
					userID := token.Subject()
					email, _ := token.Get("email")
					emailStr, _ := email.(string)
					if userID != "" {
						ctx := context.WithValue(r.Context(), UserContextKey, &models.User{
							ID:    userID,
							Email: emailStr,
						})
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// Fallback: X-API-Key
			if apiKey != "" && db != nil {
				hash := sha256.Sum256([]byte(apiKey))
				keyHash := hex.EncodeToString(hash[:])
				key, err := db.GetAPIKeyByHash(r.Context(), keyHash)
				if err == nil && key != nil {
					_ = db.UpdateAPIKeyLastUsed(r.Context(), key.ID)
					ctx := context.WithValue(r.Context(), UserContextKey, &models.User{
						ID:    key.UserID,
						Email: "",
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			jsonErr(w, http.StatusUnauthorized, "missing or invalid authentication")
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
