package services

import (
	"encoding/json"
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

// GetUserIDByEmail looks up a user by email via Supabase Auth Admin API.
// Returns the user ID if found, empty string otherwise.
func (a *AuthService) GetUserIDByEmail(email string) (string, error) {
	baseURL := strings.TrimSuffix(a.cfg.SupabaseURL, "/")
	// Supabase Auth Admin listUsers - filter by email (format may vary by version)
	reqURL := baseURL + "/auth/v1/admin/users?per_page=1000"

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.SupabaseServiceRoleKey)
	req.Header.Set("apikey", a.cfg.SupabaseServiceRoleKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("auth admin: %d %s", resp.StatusCode, string(body))
	}

	var result struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	emailLower := strings.ToLower(strings.TrimSpace(email))
	for _, u := range result.Users {
		if strings.ToLower(u.Email) == emailLower {
			return u.ID, nil
		}
	}
	return "", nil
}
