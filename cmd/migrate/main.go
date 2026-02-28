package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"quoteflow-backend/config"
)

// fixConnStr URL-encodes the password in postgres:// URLs (handles ^, &, etc.)
func fixConnStr(s string) string {
	if !strings.HasPrefix(s, "postgresql://") && !strings.HasPrefix(s, "postgres://") {
		return s
	}
	prefix := "postgresql://"
	if strings.HasPrefix(s, "postgres://") {
		prefix = "postgres://"
	}
	rest := strings.TrimPrefix(s, prefix)
	query := ""
	if idx := strings.Index(rest, "?"); idx != -1 {
		query = rest[idx:]
		rest = rest[:idx]
	}
	atIdx := strings.LastIndex(rest, "@")
	if atIdx == -1 {
		return s
	}
	userinfo, hostDB := rest[:atIdx], rest[atIdx+1:]
	user, pass, ok := strings.Cut(userinfo, ":")
	if !ok {
		return s
	}
	userEnc := url.UserPassword(user, pass).String()
	return prefix + userEnc + "@" + hostDB + query
}

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	connStr := fixConnStr(cfg.SupabaseDBURL)
	if !strings.Contains(connStr, "sslmode=") {
		if strings.Contains(connStr, "?") {
			connStr += "&sslmode=require"
		} else {
			connStr += "?sslmode=require"
		}
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("database ping failed: %v", err)
	}
	log.Println("Connected to database")

	// Create migrations tracking table if not exists
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)

	// Backfill: if profiles exists but schema_migrations is empty, 001 was already run
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if count == 0 {
		var exists bool
		_ = db.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='profiles')").Scan(&exists)
		if exists {
			_, _ = db.Exec("INSERT INTO schema_migrations (version) VALUES ('001_init.sql')")
			log.Println("Backfilled 001_init.sql (already applied)")
		}
	}

	migrationsDir := "db/migrations"
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		// Try from project root when run from cmd/migrate
		migrationsDir = filepath.Join("..", "db", "migrations")
		if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
			log.Fatalf("migrations dir not found: db/migrations")
		}
	}

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		log.Fatalf("read migrations dir: %v", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		path := filepath.Join(migrationsDir, name)
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read %s: %v", path, err)
		}

		var applied int
		err = db.QueryRow("SELECT 1 FROM schema_migrations WHERE version = $1", name).Scan(&applied)
		if err == nil {
			log.Printf("Skipping (already applied): %s", name)
			continue
		}

		log.Printf("Running migration: %s", name)
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			log.Fatalf("migration %s failed: %v", name, err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
			log.Fatalf("record migration %s: %v", name, err)
		}
		log.Printf("  ✓ %s", name)
	}

	fmt.Println("All migrations completed successfully.")
}
