package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"quoteflow-backend/config"
	"quoteflow-backend/internal/middleware"
	"quoteflow-backend/internal/models"
	"quoteflow-backend/internal/repository"
	"quoteflow-backend/internal/services"
)

var validate = validator.New()

// Handler holds all dependencies for the HTTP handlers.
type Handler struct {
	db    *repository.DB
	notif *services.NotificationService
	auth  *services.AuthService
	cfg   *config.Config
}

func New(db *repository.DB, notif *services.NotificationService, auth *services.AuthService, cfg *config.Config) *Handler {
	return &Handler{db: db, notif: notif, auth: auth, cfg: cfg}
}

// ─────────────────────────────────────────────────────────────────────────────
// HELPERS
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) json(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) ok(w http.ResponseWriter, data interface{}) {
	h.json(w, http.StatusOK, models.APIResponse{Success: true, Data: data})
}

func (h *Handler) created(w http.ResponseWriter, data interface{}) {
	h.json(w, http.StatusCreated, models.APIResponse{Success: true, Data: data})
}

func (h *Handler) err(w http.ResponseWriter, status int, msg string) {
	h.json(w, status, models.APIResponse{Success: false, Error: msg})
}

func (h *Handler) decode(r *http.Request, dest interface{}) error {
	return json.NewDecoder(r.Body).Decode(dest)
}

func (h *Handler) validateRequest(v interface{}) error {
	return validate.Struct(v)
}

func validationErrorMsg(err error) string {
	if ve, ok := err.(validator.ValidationErrors); ok {
		msgs := make([]string, 0, len(ve))
		for _, fe := range ve {
			switch fe.Tag() {
			case "required":
				msgs = append(msgs, fmt.Sprintf("%s is required", fe.Field()))
			case "email":
				msgs = append(msgs, fmt.Sprintf("%s must be a valid email", fe.Field()))
			case "uuid":
				msgs = append(msgs, fmt.Sprintf("%s must be a valid UUID", fe.Field()))
			case "oneof":
				msgs = append(msgs, fmt.Sprintf("%s must be one of: %s", fe.Field(), fe.Param()))
			case "min":
				msgs = append(msgs, fmt.Sprintf("%s must be at least %s", fe.Field(), fe.Param()))
			case "max":
				msgs = append(msgs, fmt.Sprintf("%s must be at most %s", fe.Field(), fe.Param()))
			default:
				msgs = append(msgs, fmt.Sprintf("%s failed %s validation", fe.Field(), fe.Tag()))
			}
		}
		return strings.Join(msgs, "; ")
	}
	return err.Error()
}

func currentUser(r *http.Request) *models.User {
	user, _ := middleware.UserFromContext(r.Context())
	return user
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH — Supabase handles registration/login directly from the frontend
// These endpoints are thin wrappers / profile bootstrap helpers.
// ─────────────────────────────────────────────────────────────────────────────

// GET /auth/me — returns current user + profile
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	profile, err := h.db.GetProfile(r.Context(), user.ID)
	if err != nil {
		// Profile auto-created by DB trigger; return minimal data
		h.ok(w, map[string]interface{}{"user": user, "profile": nil})
		return
	}
	h.ok(w, map[string]interface{}{"user": user, "profile": profile})
}

// DELETE /user — deletes the current user's account and all their data
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if err := h.auth.DeleteUser(user.ID); err != nil {
		msg := "failed to delete account"
		if h.cfg.IsDevelopment() {
			msg = err.Error()
		}
		h.err(w, http.StatusInternalServerError, msg)
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// DASHBOARD
// ─────────────────────────────────────────────────────────────────────────────

// GET /dashboard?currency=JMD — aggregated stats + recent activity. currency optional: when present filter monetary stats; when absent ("All") zero out money fields.
func (h *Handler) GetDashboard(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	currency := r.URL.Query().Get("currency")
	stats, err := h.db.GetDashboardStats(r.Context(), user.ID, currency)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load dashboard")
		return
	}
	h.ok(w, stats)
}

// ─────────────────────────────────────────────────────────────────────────────
// PROFILE / SETTINGS
// ─────────────────────────────────────────────────────────────────────────────

// GET /profile
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	profile, err := h.db.GetProfile(r.Context(), user.ID)
	if err != nil {
		// New users may not have a profile yet; return 404 so frontend can show empty form
		h.err(w, http.StatusNotFound, "profile not found")
		return
	}
	h.ok(w, profile)
}

// PUT /profile — full upsert
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.Profile
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.UserID = user.ID
	if err := h.db.UpsertProfile(r.Context(), &req); err != nil {
		msg := "failed to save profile"
		if h.cfg.IsDevelopment() {
			msg = err.Error()
		}
		h.err(w, http.StatusInternalServerError, msg)
		return
	}
	h.ok(w, req)
}

// ─────────────────────────────────────────────────────────────────────────────
// CLIENTS
// ─────────────────────────────────────────────────────────────────────────────

// GET /clients
func (h *Handler) ListClients(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	clients, err := h.db.ListClients(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load clients")
		return
	}
	h.ok(w, clients)
}

// GET /clients/:id
func (h *Handler) GetClient(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	client, err := h.db.GetClient(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "client not found")
		return
	}
	h.ok(w, client)
}

// POST /clients
func (h *Handler) CreateClient(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.CreateClientRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	client := &models.Client{
		UserID:  user.ID,
		Name:    strings.TrimSpace(req.Name),
		Company: strings.TrimSpace(req.Company),
		Email:   strings.TrimSpace(req.Email),
		Phone:   req.Phone,
		Address: req.Address,
		Notes:   req.Notes,
	}
	if err := h.db.CreateClient(r.Context(), client); err != nil {
		msg := "failed to create client"
		if h.cfg.IsDevelopment() {
			msg = err.Error()
		}
		h.err(w, http.StatusInternalServerError, msg)
		return
	}

	_ = h.db.LogEvent(r.Context(), user.ID, "", "client_added",
		fmt.Sprintf("New client added: %s", client.Name))

	h.created(w, client)
}

// PUT /clients/:id
func (h *Handler) UpdateClient(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	var req models.Client
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ID = id
	req.UserID = user.ID
	if err := h.db.UpdateClient(r.Context(), &req); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to update client")
		return
	}
	h.ok(w, req)
}

// DELETE /clients/:id
func (h *Handler) DeleteClient(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteClient(r.Context(), id, user.ID); err != nil {
		if strings.Contains(err.Error(), "foreign key") || strings.Contains(err.Error(), "violates") || strings.Contains(err.Error(), "restrict") {
			h.err(w, http.StatusConflict, "cannot delete client with existing quotes — delete or reassign their quotes first")
			return
		}
		h.err(w, http.StatusInternalServerError, "failed to delete client")
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTES
// ─────────────────────────────────────────────────────────────────────────────

// GET /quotes?status=all|draft|sent|accepted|expired&currency=JMD
func (h *Handler) ListQuotes(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	status := r.URL.Query().Get("status")
	currency := r.URL.Query().Get("currency")
	quotes, err := h.db.ListQuotes(r.Context(), user.ID, status, currency)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load quotes")
		return
	}
	h.ok(w, quotes)
}

// GET /quotes/export — streams CSV file
func (h *Handler) ExportQuotesCSV(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	quotes, err := h.db.ListQuotes(r.Context(), user.ID, "", "")
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load quotes")
		return
	}
	csvData, err := services.ExportQuotesCSV(quotes)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate CSV")
		return
	}
	filename := fmt.Sprintf("quoteflow-quotes-%s.csv", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(csvData)))
	w.Write(csvData)
}

// GET /quotes/:id
func (h *Handler) GetQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	h.ok(w, quote)
}

// POST /quotes — create new quote with line items in one call
func (h *Handler) CreateQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.CreateQuoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}

	// Free tier enforcement: max 3 quotes/month for non-Pro users
	// TODO: once Stripe is integrated, check user's plan from profiles.plan column
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	isPro := profile != nil && profile.Plan == "pro"
	if !isPro {
		count, err := h.db.CountQuotesThisMonth(r.Context(), user.ID)
		if err == nil && count >= 3 {
			h.json(w, http.StatusPaymentRequired, models.APIResponse{
				Success: false,
				Error:   "free_tier_limit",
				Message: "You've reached the free tier limit of 3 quotes per month. Upgrade to Pro for unlimited quotes.",
			})
			return
		}
	}

	// Get next quote number
	quoteNum, err := h.db.NextQuoteNumber(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate quote number")
		return
	}

	quote := &models.Quote{
		UserID:           user.ID,
		ClientID:         req.ClientID,
		QuoteNumber:      quoteNum,
		Title:            req.Title,
		Status:           models.StatusDraft,
		Currency:         req.Currency,
		ValidityDays:     req.ValidityDays,
		Notes:            req.Notes,
		Deposit:          req.Deposit,
		PaymentMethod:    req.PaymentMethod,
		DeliveryTimeline: req.DeliveryTimeline,
		Revisions:        req.Revisions,
		TaxExempt:        req.TaxExempt,
		TaxRate:          req.TaxRate,
		RequireSignature: req.RequireSignature,
		TrackViews:       req.TrackViews,
		SendReminder:     req.SendReminder,
	}

	if err := h.db.CreateQuote(r.Context(), quote, req.LineItems); err != nil {
		msg := "failed to create quote"
		if h.cfg.IsDevelopment() {
			msg = err.Error()
		}
		h.err(w, http.StatusInternalServerError, msg)
		return
	}

	_ = h.db.LogEvent(r.Context(), user.ID, quote.ID, "created",
		fmt.Sprintf("Quote %s created", quote.QuoteNumber))

	// Return full quote with client + line_items
	full, _ := h.db.GetQuote(r.Context(), quote.ID, user.ID)
	if full != nil {
		h.created(w, full)
	} else {
		h.created(w, quote)
	}
}

// PATCH /quotes/:id — update quote fields and optionally replace line items
func (h *Handler) UpdateQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")

	var req models.UpdateQuoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.db.UpdateQuote(r.Context(), id, user.ID, &req)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to update quote")
		return
	}

	_ = h.db.LogEvent(r.Context(), user.ID, id, "updated",
		fmt.Sprintf("Quote %s updated", updated.QuoteNumber))

	h.ok(w, updated)
}

// POST /quotes/:id/send — send quote via email, whatsapp, or return link
func (h *Handler) SendQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")

	var req models.SendQuoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}

	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}

	// Get sender's profile for their name
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	senderName := user.Email
	if profile != nil && profile.BusinessName != "" {
		senderName = profile.BusinessName
	}

	quoteLink := fmt.Sprintf("%s/%s", h.cfg.QuoteLinkBaseURL, quote.ShareToken)

	switch req.Channel {
	case "email":
		recipient := req.RecipientEmail
		if recipient == "" {
			recipient = quote.Client.Email
		}
		if err := h.notif.SendQuoteByEmail(quote, recipient, senderName); err != nil {
			h.err(w, http.StatusInternalServerError, "failed to send email: "+err.Error())
			return
		}
	case "whatsapp":
		phone := req.RecipientPhone
		if phone == "" {
			phone = quote.Client.Phone
		}
		if phone == "" {
			h.err(w, http.StatusBadRequest, "recipient phone number is required for WhatsApp")
			return
		}
		if err := h.notif.SendQuoteViaWhatsApp(quote, phone, senderName); err != nil {
			h.err(w, http.StatusInternalServerError, "failed to send WhatsApp: "+err.Error())
			return
		}
	case "link":
		// Just return the link — no external send needed
	default:
		h.err(w, http.StatusBadRequest, "channel must be email, whatsapp, or link")
		return
	}

	// Update quote status to sent
	if quote.Status == models.StatusDraft {
		_ = h.db.UpdateQuoteStatus(r.Context(), id, user.ID, models.StatusSent)
	}

	_ = h.db.LogEvent(r.Context(), user.ID, id, "sent",
		fmt.Sprintf("Quote %s sent via %s to %s",
			quote.QuoteNumber, req.Channel, quote.Client.Name))

	h.ok(w, map[string]string{
		"message":    "quote sent successfully",
		"quote_link": quoteLink,
		"channel":    req.Channel,
	})
}

// POST /quotes/:id/duplicate
func (h *Handler) DuplicateQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")

	// Free tier enforcement: same limit as CreateQuote
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	isPro := profile != nil && profile.Plan == "pro"
	if !isPro {
		count, err := h.db.CountQuotesThisMonth(r.Context(), user.ID)
		if err == nil && count >= 3 {
			h.json(w, http.StatusPaymentRequired, models.APIResponse{
				Success: false,
				Error:   "free_tier_limit",
				Message: "You've reached the free tier limit of 3 quotes per month. Upgrade to Pro for unlimited quotes.",
			})
			return
		}
	}

	newQuote, err := h.db.DuplicateQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to duplicate quote")
		return
	}

	_ = h.db.LogEvent(r.Context(), user.ID, newQuote.ID, "duplicated",
		fmt.Sprintf("Quote %s duplicated as %s", id[:8], newQuote.QuoteNumber))

	h.created(w, newQuote)
}

// POST /quotes/:id/mark-paid — mark an accepted quote as paid
func (h *Handler) MarkQuoteAsPaid(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	quote, err := h.db.MarkQuoteAsPaid(r.Context(), id, user.ID)
	if err != nil {
		if strings.Contains(err.Error(), "must be accepted") {
			h.err(w, http.StatusBadRequest, "quote must be accepted before marking as paid")
			return
		}
		h.err(w, http.StatusInternalServerError, "failed to mark quote as paid")
		return
	}
	_ = h.db.LogEvent(r.Context(), user.ID, id, "paid",
		fmt.Sprintf("Quote %s marked as paid", quote.QuoteNumber))
	h.ok(w, quote)
}

// DELETE /quotes/:id
func (h *Handler) DeleteQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteQuote(r.Context(), id, user.ID); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to delete quote")
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// PUBLIC QUOTE VIEWER (no auth — accessed via share token)
// ─────────────────────────────────────────────────────────────────────────────

// GET /q/:token — returns quote data for the public viewer
func (h *Handler) PublicGetQuote(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	quote, err := h.db.GetQuoteByShareToken(r.Context(), token)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found or link has expired")
		return
	}

	if quote.TrackViews {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = h.db.IncrementViewCount(bgCtx, quote.ID)
			if profile, err := h.db.GetProfile(bgCtx, quote.UserID); err == nil {
				_ = h.notif.SendQuoteViewedNotification(&quote.Quote, quote.Client.Name, profile.EmailOnQuote)
			}
		}()
	}

	// Include creator profile (logo, business name) for display on public quote
	profile, _ := h.db.GetProfile(r.Context(), quote.UserID)
	type creatorInfo struct {
		LogoURL     *string `json:"logo_url,omitempty"`
		BusinessName string  `json:"business_name,omitempty"`
		BrandColor  string  `json:"brand_color,omitempty"`
	}
	out := struct {
		models.QuoteWithDetails
		Creator *creatorInfo `json:"creator,omitempty"`
	}{QuoteWithDetails: *quote}
	if profile != nil {
		out.Creator = &creatorInfo{LogoURL: profile.LogoURL, BusinessName: profile.BusinessName, BrandColor: profile.BrandColor}
	}
	h.ok(w, &out)
}

// POST /q/:token/accept — client accepts the quote
func (h *Handler) PublicAcceptQuote(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")

	var req models.AcceptQuoteRequest
	_ = h.decode(r, &req) // optional signature name

	quote, err := h.db.AcceptQuote(r.Context(), token, req.SignatureName)
	if err != nil {
		h.err(w, http.StatusBadRequest, "could not accept quote — it may have expired or already been accepted")
		return
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		fullQuote, err := h.db.GetQuote(bgCtx, quote.ID, quote.UserID)
		if err != nil {
			return
		}
		profile, err := h.db.GetProfile(bgCtx, quote.UserID)
		if err != nil {
			return
		}
		_ = h.notif.SendQuoteAcceptedNotification(fullQuote, profile.EmailOnQuote)
		signer := quote.AcceptedByName
		if signer == "" && fullQuote.Client.Name != "" {
			signer = fullQuote.Client.Name
		}
		if signer == "" {
			signer = "Client"
		}
		_ = h.db.LogEvent(bgCtx, quote.UserID, quote.ID, "accepted",
			fmt.Sprintf("%s accepted quote %s", signer, quote.QuoteNumber))
	}()

	h.ok(w, map[string]interface{}{
		"accepted": true,
		"quote_number": quote.QuoteNumber,
		"message": "Thank you! Your acceptance has been recorded. The freelancer has been notified.",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// HEALTH CHECK
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	h.ok(w, map[string]string{
		"status":  "ok",
		"service": "quoteflow-api",
		"time":    time.Now().Format(time.RFC3339),
	})
}
