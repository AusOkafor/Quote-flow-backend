package services

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"quoteflow-backend/config"
)

// AuthService handles auth-related operations via Supabase Admin API.
type AuthService struct {
	cfg *config.Config
}

// NewAuthService creates a new AuthService.
func NewAuthService(cfg *config.Config) *AuthService {
	return &AuthService{cfg: cfg}
}

// DeleteUser deletes a user from Supabase Auth (auth.users).
// Requires service role key. Cascades to profiles, clients, quotes, etc. via DB FKs.
func (a *AuthService) DeleteUser(userID string) error {
	baseURL := strings.TrimSuffix(a.cfg.SupabaseURL, "/")
	url := baseURL + "/auth/v1/admin/users/" + userID

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.SupabaseServiceRoleKey)
	req.Header.Set("apikey", a.cfg.SupabaseServiceRoleKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete user failed (%d): %s", resp.StatusCode, string(body))
}
