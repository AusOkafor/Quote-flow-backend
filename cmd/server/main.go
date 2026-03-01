package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/handlers"
	"quoteflow-backend/internal/middleware"
	"quoteflow-backend/internal/repository"
	"quoteflow-backend/internal/services"
)

func main() {
	// ── Config ───────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// ── Dependencies ─────────────────────────────────────────
	db, err := repository.New(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	notif := services.NewNotificationService(cfg)
	auth  := services.NewAuthService(cfg)
	h     := handlers.New(db, notif, auth, cfg)

	jwtVerifier, err := middleware.NewJWTVerifier(cfg)
	if err != nil {
		log.Fatalf("jwt verifier: %v", err)
	}

	// ── Rate limiters ────────────────────────────────────────
	publicLimiter := middleware.NewRateLimiter(10, 20)   // 10 req/s per IP, burst 20
	apiLimiter    := middleware.NewRateLimiter(30, 60)    // 30 req/s per IP, burst 60

	// ── Router ───────────────────────────────────────────────
	r := chi.NewRouter()

	// Global middleware stack
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID", "X-API-Key"},
		ExposedHeaders:   []string{"Content-Disposition", "Content-Length"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ── Public routes (no auth) ───────────────────────────────

	r.Get("/health", h.HealthCheck)

	// Internal cron (CRON_SECRET auth)
	r.Route("/internal/cron", func(r chi.Router) {
		r.Use(middleware.RequireCronSecret(cfg))
		r.Post("/reminders", h.CronReminders)
	})

	// Public quote viewer — clients open these from WhatsApp/email links
	r.Route("/q/{token}", func(r chi.Router) {
		r.Use(publicLimiter.Limit)
		r.Get("/",        h.PublicGetQuote)    // load quote data for viewer
		r.Post("/accept", h.PublicAcceptQuote) // client accepts the quote
		r.Get("/notes",   h.PublicGetNotes)    // notes thread (public)
		r.Post("/notes",  h.PublicPostNote)    // client posts note (public)
	})

	// ── Protected routes (JWT required) ──────────────────────
	r.Group(func(r chi.Router) {
		r.Use(apiLimiter.Limit)
		r.Use(middleware.RequireAuth(jwtVerifier, db))

		// Auth
		r.Get("/auth/me", h.GetMe)
		r.Delete("/user", h.DeleteUser)

		// Dashboard & analytics
		r.Get("/dashboard", h.GetDashboard)
		r.Get("/dashboard/unread-messages", h.GetUnreadMessages)

		// Profile / settings
		r.Get("/profile", h.GetProfile)
		r.Put("/profile", h.UpdateProfile)

		// Teams (Business plan)
		r.Get("/teams", h.GetMyTeam)
		r.Get("/teams/{id}/members", h.ListTeamMembers)
		r.Post("/teams/{id}/members", h.AddTeamMember)
		r.Delete("/teams/{id}/members/{userId}", h.RemoveTeamMember)

		// API keys (Business plan)
		r.Get("/api-keys", h.ListAPIKeys)
		r.Post("/api-keys", h.CreateAPIKey)
		r.Delete("/api-keys/{id}", h.DeleteAPIKey)

		// Templates
		r.Get("/templates", h.ListTemplates)
		r.Post("/templates", h.CreateTemplate)
		r.Post("/templates/from-quote", h.CreateTemplateFromQuote)
		r.Delete("/templates/{id}", h.DeleteTemplate)

		// Clients
		r.Route("/clients", func(r chi.Router) {
			r.Get("/",        h.ListClients)
			r.Post("/",       h.CreateClient)
			r.Get("/{id}",    h.GetClient)
			r.Put("/{id}",    h.UpdateClient)
			r.Delete("/{id}", h.DeleteClient)
		})

		// Quotes
		r.Route("/quotes", func(r chi.Router) {
			r.Get("/",       h.ListQuotes)
			r.Post("/",      h.CreateQuote)
			r.Get("/export", h.ExportQuotesCSV)

			r.Route("/{id}", func(r chi.Router) {
				r.Get("/",             h.GetQuote)
				r.Patch("/",           h.UpdateQuote)
				r.Delete("/",          h.DeleteQuote)
				r.Post("/send",        h.SendQuote)
				r.Post("/duplicate",   h.DuplicateQuote)
				r.Post("/mark-paid",   h.MarkQuoteAsPaid)
				r.Get("/notes",        h.GetNotes)
				r.Post("/notes",       h.PostNote)
				r.Patch("/notes/read", h.MarkNotesRead)
			})
		})
	})

	// 404
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"error":"route not found"}`))
	})

	// ── HTTP Server ───────────────────────────────────────────
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start in background, block on OS signal
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("🚀  QuoteFlow API  :%s  [%s]", cfg.Port, cfg.Env)
		log.Printf("    Frontend URL : %s", cfg.FrontendURL)
		log.Printf("    Supabase     : %s", cfg.SupabaseURL)
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	case sig := <-quit:
		log.Printf("signal %v received — shutting down gracefully…", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("Server stopped.")
}
