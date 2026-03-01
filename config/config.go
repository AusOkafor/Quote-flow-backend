package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Server
	Port string
	Env  string

	// Supabase
	SupabaseURL             string
	SupabaseAnonKey         string
	SupabaseServiceRoleKey  string
	SupabaseDBURL           string

	// JWT
	JWTSecret string

	// Email (Resend)
	ResendAPIKey  string
	EmailFrom     string
	EmailFromName string

	// WhatsApp (Twilio)
	TwilioAccountSID      string
	TwilioAuthToken       string
	TwilioWhatsAppNumber  string

	// App URLs
	AppURL           string
	FrontendURL      string
	QuoteLinkBaseURL string

	// CORS
	AllowedOrigins []string

	// Dev only: user ID that bypasses free tier limits (unlimited quotes, etc.)
	DevBypassUserID string

	// Cron: secret for internal cron endpoints (e.g. reminders)
	CronSecret string
}

// Load reads .env file (if present) and populates Config from environment variables.
func Load() (*Config, error) {
	// Load .env file — ignore error in production (vars already set)
	_ = godotenv.Load()

	cfg := &Config{
		Port:                    getEnv("PORT", "8081"),
		Env:                     getEnv("ENV", "development"),
		SupabaseURL:             mustGetEnv("SUPABASE_URL"),
		SupabaseAnonKey:         mustGetEnv("SUPABASE_ANON_KEY"),
		SupabaseServiceRoleKey:  mustGetEnv("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseDBURL:           mustGetEnv("SUPABASE_DB_URL"),
		JWTSecret:               getEnv("JWT_SECRET", ""), // Optional: used only for legacy HS256; ES256 uses JWKS
		ResendAPIKey:            getEnv("RESEND_API_KEY", ""),
		EmailFrom:               getEnv("EMAIL_FROM", "quotes@quoteflow.app"),
		EmailFromName:           getEnv("EMAIL_FROM_NAME", "QuoteFlow"),
		TwilioAccountSID:        getEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:         getEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioWhatsAppNumber:    getEnv("TWILIO_WHATSAPP_NUMBER", ""),
		AppURL:                  getEnv("APP_URL", "http://localhost:8080"),
		FrontendURL:             getEnv("FRONTEND_URL", "https://quote-flow-phi.vercel.app"),
		QuoteLinkBaseURL:        getEnv("QUOTE_LINK_BASE_URL", "https://quote-flow-phi.vercel.app/q"),
		DevBypassUserID:         getEnv("DEV_BYPASS_USER_ID", "4946c101-f0e4-4c6d-a094-012a6ff7775a"),
		CronSecret:              getEnv("CRON_SECRET", ""),
	}

	// Parse comma-separated allowed origins
	rawOrigins := getEnv("ALLOWED_ORIGINS", cfg.FrontendURL)
	for _, o := range strings.Split(rawOrigins, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, trimmed)
		}
	}

	return cfg, nil
}

func (c *Config) IsDevelopment() bool { return c.Env == "development" }
func (c *Config) IsProduction() bool  { return c.Env == "production" }

// IsDevBypassUser returns true if userID is the dev bypass user (only in development).
func (c *Config) IsDevBypassUser(userID string) bool {
	return c.IsDevelopment() && c.DevBypassUserID != "" && userID == c.DevBypassUserID
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func mustGetEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return val
}
