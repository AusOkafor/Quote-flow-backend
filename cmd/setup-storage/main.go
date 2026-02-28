// setup-storage creates the logos bucket in Supabase Storage.
// Run once: go run ./cmd/setup-storage
//
// Alternative: create manually in Supabase Dashboard → Storage → New bucket
// Name: logos, Public: true, File size limit: 2MB, Allowed MIME: image/png, image/jpeg, image/jpg, image/svg+xml
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/joho/godotenv"
	"quoteflow-backend/config"
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	serviceKey := strings.Trim(strings.TrimSpace(cfg.SupabaseServiceRoleKey), `"'`)
	baseURL := strings.TrimSuffix(strings.TrimSpace(cfg.SupabaseURL), "/")
	storageURL := baseURL + "/storage/v1"

	parts := strings.Split(serviceKey, ".")
	if len(parts) != 3 {
		log.Printf("Warning: SUPABASE_SERVICE_ROLE_KEY does not look like a JWT (expected 3 dot-separated parts). Ensure you use the service_role key from Supabase Dashboard → Settings → API.")
	}

	body := map[string]interface{}{
		"id":                 "logos",
		"name":               "logos",
		"public":             true,
		"file_size_limit":    2 * 1024 * 1024, // 2MB
		"allowed_mime_types": []string{"image/png", "image/jpeg", "image/jpg", "image/svg+xml"},
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, storageURL+"/bucket", bytes.NewReader(bodyJSON))
	if err != nil {
		log.Fatalf("create request: %v", err)
	}
	req.Header.Set("apikey", serviceKey)
	req.Header.Set("Authorization", "Bearer "+serviceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		fmt.Println("✅ Created storage bucket 'logos'")
		return
	}

	errMsg := string(respBody)
	if strings.Contains(errMsg, "already exists") || strings.Contains(strings.ToLower(errMsg), "duplicate") {
		log.Println("Bucket 'logos' already exists — OK")
		return
	}

	if resp.StatusCode == 401 || strings.Contains(errMsg, "Invalid") || strings.Contains(errMsg, "JWT") {
		log.Fatalf("Auth failed (status %d). Check SUPABASE_SERVICE_ROLE_KEY in .env — use the service_role key from Supabase Dashboard → Settings → API, not the anon key.\nResponse: %s", resp.StatusCode, errMsg)
	}

	log.Fatalf("create bucket failed (status %d): %s", resp.StatusCode, errMsg)
}
