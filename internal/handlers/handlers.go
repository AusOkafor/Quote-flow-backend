package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
	"github.com/stripe/stripe-go/v76/webhook"
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

// isBusiness returns true if the user has Business plan or is the dev bypass user.
func (h *Handler) isBusiness(userID string, profile *models.Profile) bool {
	return (profile != nil && profile.Plan == "business") || h.cfg.IsDevBypassUser(userID)
}

func getSubscriptionCustomerID(sub *stripe.Subscription, raw json.RawMessage) string {
	if sub.Customer != nil {
		return sub.Customer.ID
	}
	var m struct {
		Customer string `json:"customer"`
	}
	if json.Unmarshal(raw, &m) == nil {
		return m.Customer
	}
	return ""
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
	if h.cfg.IsDevBypassUser(user.ID) {
		p := *profile
		p.Plan = "business"
		profile = &p
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

// GET /dashboard/unread-messages — unread client notes for toast notifications
func (h *Handler) GetUnreadMessages(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	msgs, err := h.db.GetUnreadClientMessages(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load unread messages")
		return
	}
	if msgs == nil {
		msgs = []models.UnreadClientMessage{}
	}
	h.ok(w, msgs)
}

// POST /internal/cron/reminders — sends client reminders and freelancer expiring notifications.
// Protected by CRON_SECRET. Invoke via external cron (Render, GitHub Actions, etc.).
func (h *Handler) CronReminders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sent := 0
	errs := []string{}

	// 1. Client reminders (send_reminder=true, expires in 3 days)
	clientQuotes, err := h.db.GetQuotesNeedingClientReminder(ctx)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load quotes for reminders")
		return
	}
	for _, quote := range clientQuotes {
		profile, _ := h.db.GetProfile(ctx, quote.UserID)
		senderName := "QuoteFlow"
		if profile != nil && profile.BusinessName != "" {
			senderName = profile.BusinessName
		}
		if err := h.notif.SendExpiryReminderToClient(&quote, senderName); err != nil {
			errs = append(errs, fmt.Sprintf("client reminder %s: %v", quote.QuoteNumber, err))
			continue
		}
		_ = h.db.MarkReminderSent(ctx, quote.ID)
		sent++
	}

	// 2. Freelancer expiring notifications (notify_expiring=true, no expiring event yet)
	freelancerQuotes, err := h.db.GetQuotesNeedingFreelancerExpiringNotification(ctx)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load quotes for expiring notifications")
		return
	}
	for _, quote := range freelancerQuotes {
		profile, _ := h.db.GetProfile(ctx, quote.UserID)
		email := ""
		if profile != nil && profile.EmailOnQuote != "" {
			email = profile.EmailOnQuote
		}
		if email == "" {
			// Fallback: we don't have user email in quote; would need auth.users lookup
			continue
		}
		if err := h.notif.SendExpiringSoonToFreelancer(&quote, email); err != nil {
			errs = append(errs, fmt.Sprintf("freelancer expiring %s: %v", quote.QuoteNumber, err))
			continue
		}
		_ = h.db.LogEvent(ctx, quote.UserID, quote.ID, "expiring",
			fmt.Sprintf("Quote %s expires in 3 days", quote.QuoteNumber))
		sent++
	}

	h.ok(w, map[string]interface{}{
		"sent":   sent,
		"errors": errs,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TEAMS (Business plan)
// ─────────────────────────────────────────────────────────────────────────────

// GET /teams — returns current user's team
func (h *Handler) GetMyTeam(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	team, err := h.db.GetTeamByUserID(r.Context(), user.ID)
	if err != nil || team == nil {
		h.ok(w, nil)
		return
	}
	h.ok(w, team)
}

// GET /teams/:id/members
func (h *Handler) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	ok, _ := h.db.IsTeamMember(r.Context(), id, user.ID)
	if !ok {
		h.err(w, http.StatusForbidden, "not a team member")
		return
	}
	members, err := h.db.ListTeamMembers(r.Context(), id)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load members")
		return
	}
	h.ok(w, members)
}

// POST /teams/:id/members — invite by email (Business only, max 5)
func (h *Handler) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	if !h.isBusiness(user.ID, profile) {
		h.json(w, http.StatusPaymentRequired, models.APIResponse{
			Success: false,
			Error:   "business_required",
			Message: "Team members require a Business plan.",
		})
		return
	}
	ok, _ := h.db.IsTeamMember(r.Context(), id, user.ID)
	if !ok {
		h.err(w, http.StatusForbidden, "not a team member")
		return
	}
	count, _ := h.db.CountTeamMembers(r.Context(), id)
	if count >= 5 {
		h.err(w, http.StatusBadRequest, "team limit reached (max 5 members)")
		return
	}
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := h.decode(r, &req); err != nil || req.Email == "" {
		h.err(w, http.StatusBadRequest, "email required")
		return
	}
	inviteeID, err := h.auth.GetUserIDByEmail(strings.TrimSpace(req.Email))
	if err != nil || inviteeID == "" {
		h.err(w, http.StatusNotFound, "user not found with that email")
		return
	}
	if inviteeID == user.ID {
		h.err(w, http.StatusBadRequest, "cannot add yourself")
		return
	}
	role := "member"
	if req.Role == "admin" {
		role = "admin"
	}
	if err := h.db.AddTeamMember(r.Context(), id, inviteeID, role); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			h.err(w, http.StatusConflict, "user already in team")
			return
		}
		h.err(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	members, _ := h.db.ListTeamMembers(r.Context(), id)
	h.ok(w, members)
}

// DELETE /teams/:id/members/:userId
func (h *Handler) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	targetID := chi.URLParam(r, "userId")
	ok, _ := h.db.IsTeamMember(r.Context(), id, user.ID)
	if !ok {
		h.err(w, http.StatusForbidden, "not a team member")
		return
	}
	if targetID == user.ID {
		h.err(w, http.StatusBadRequest, "cannot remove yourself — transfer ownership first")
		return
	}
	if err := h.db.RemoveTeamMember(r.Context(), id, targetID); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to remove member")
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// API KEYS (Business plan)
// ─────────────────────────────────────────────────────────────────────────────

// GET /api-keys
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	if !h.isBusiness(user.ID, profile) {
		h.json(w, http.StatusPaymentRequired, models.APIResponse{
			Success: false,
			Error:   "business_required",
			Message: "API keys require a Business plan.",
		})
		return
	}
	keys, err := h.db.ListAPIKeys(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load API keys")
		return
	}
	h.ok(w, keys)
}

// POST /api-keys
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	if !h.isBusiness(user.ID, profile) {
		h.json(w, http.StatusPaymentRequired, models.APIResponse{
			Success: false,
			Error:   "business_required",
			Message: "API keys require a Business plan.",
		})
		return
	}
	var req models.CreateAPIKeyRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	// Generate key: qf_live_ + 32 random bytes (hex)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate key")
		return
	}
	rawKey := "qf_live_" + hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(hash[:])
	created, err := h.db.CreateAPIKey(r.Context(), user.ID, req.Name, keyHash)
	if err != nil {
		errMsg := err.Error()
		if h.cfg.IsDevelopment() {
			h.err(w, http.StatusInternalServerError, "failed to create API key: "+errMsg)
		} else {
			h.err(w, http.StatusInternalServerError, "failed to create API key")
		}
		return
	}
	h.created(w, models.CreateAPIKeyResponse{
		ID:        created.ID,
		Name:      created.Name,
		Key:       rawKey,
		CreatedAt: created.CreatedAt,
	})
}

// DELETE /api-keys/:id
func (h *Handler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	if !h.isBusiness(user.ID, profile) {
		h.json(w, http.StatusPaymentRequired, models.APIResponse{
			Success: false,
			Error:   "business_required",
			Message: "API keys require a Business plan.",
		})
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteAPIKey(r.Context(), id, user.ID); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to revoke API key")
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// BILLING (Stripe)
// ─────────────────────────────────────────────────────────────────────────────

// CreateCheckoutSessionRequest — POST /billing/create-checkout-session
type CreateCheckoutSessionRequest struct {
	Plan     string `json:"plan" validate:"required,oneof=pro business"`
	Interval string `json:"interval" validate:"required,oneof=monthly annual"`
}

// POST /billing/create-checkout-session
func (h *Handler) CreateCheckoutSession(w http.ResponseWriter, r *http.Request) {
	if h.cfg.StripeSecretKey == "" || h.cfg.StripePriceProMonthly == "" {
		h.err(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	user := currentUser(r)
	var req CreateCheckoutSessionRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	var priceID string
	switch req.Plan + "_" + req.Interval {
	case "pro_monthly":
		priceID = h.cfg.StripePriceProMonthly
	case "pro_annual":
		priceID = h.cfg.StripePriceProAnnual
	case "business_monthly":
		priceID = h.cfg.StripePriceBusinessMonthly
	case "business_annual":
		priceID = h.cfg.StripePriceBusinessAnnual
	default:
		h.err(w, http.StatusBadRequest, "invalid plan or interval")
		return
	}
	if priceID == "" {
		h.err(w, http.StatusBadRequest, "price not configured for this plan")
		return
	}
	stripe.Key = h.cfg.StripeSecretKey
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	customerID := ""
	if profile != nil && profile.StripeCustomerID != "" {
		customerID = profile.StripeCustomerID
	} else {
		params := &stripe.CustomerParams{
			Email: stripe.String(user.Email),
			Metadata: map[string]string{
				"user_id": user.ID,
			},
		}
		c, err := customer.New(params)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "failed to create customer")
			return
		}
		customerID = c.ID
		if profile != nil {
			_ = h.db.UpdateProfilePlan(r.Context(), user.ID, profile.Plan, customerID)
		}
	}
	successURL := strings.TrimSuffix(h.cfg.FrontendURL, "/") + "/app/settings?panel=billing&success=true"
	cancelURL := strings.TrimSuffix(h.cfg.FrontendURL, "/") + "/app/settings?panel=billing"
	sessParams := &stripe.CheckoutSessionParams{
		Customer:          stripe.String(customerID),
		ClientReferenceID: stripe.String(user.ID),
		Mode:              stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		Metadata:          map[string]string{"plan": req.Plan},
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(priceID), Quantity: stripe.Int64(1)},
		},
	}
	sess, err := session.New(sessParams)
	if err != nil {
		errMsg := err.Error()
		if h.cfg.IsDevelopment() {
			h.err(w, http.StatusInternalServerError, "failed to create checkout session: "+errMsg)
		} else {
			h.err(w, http.StatusInternalServerError, "failed to create checkout session")
		}
		return
	}
	h.ok(w, map[string]string{"url": sess.URL})
}

// POST /billing/webhook — Stripe webhook (no auth, verify signature)
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if h.cfg.StripeWebhookSecret == "" {
		h.err(w, http.StatusServiceUnavailable, "webhook not configured")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.err(w, http.StatusBadRequest, "failed to read body")
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	event, err := webhook.ConstructEvent(body, sig, h.cfg.StripeWebhookSecret)
	if err != nil {
		h.err(w, http.StatusBadRequest, "invalid signature")
		return
	}
	switch event.Type {
	case "checkout.session.completed":
		var sess stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
			h.err(w, http.StatusBadRequest, "invalid payload")
			return
		}
		userID := sess.ClientReferenceID
		if userID == "" {
			userID = sess.Metadata["user_id"]
		}
		if userID == "" {
			profile, _ := h.db.GetProfileByStripeCustomerID(r.Context(), sess.Customer.ID)
			if profile != nil {
				userID = profile.UserID
			}
		}
		if userID == "" {
			h.err(w, http.StatusBadRequest, "cannot determine user")
			return
		}
		plan := "pro"
		if p, ok := sess.Metadata["plan"]; ok && p != "" {
			plan = p
		}
		custID := ""
		if sess.Customer != nil {
			custID = sess.Customer.ID
		} else {
			var raw struct {
				Customer string `json:"customer"`
			}
			_ = json.Unmarshal(event.Data.Raw, &raw)
			custID = raw.Customer
		}
		_ = h.db.UpdateProfilePlan(r.Context(), userID, plan, custID)
	case "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			h.err(w, http.StatusBadRequest, "invalid payload")
			return
		}
		custID := getSubscriptionCustomerID(&sub, event.Data.Raw)
		profile, err := h.db.GetProfileByStripeCustomerID(r.Context(), custID)
		if err != nil || profile == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = h.db.UpdateProfilePlan(r.Context(), profile.UserID, "free", profile.StripeCustomerID)
	case "customer.subscription.updated":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			h.err(w, http.StatusBadRequest, "invalid payload")
			return
		}
		custID := getSubscriptionCustomerID(&sub, event.Data.Raw)
		profile, err := h.db.GetProfileByStripeCustomerID(r.Context(), custID)
		if err != nil || profile == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		plan := "free"
		if sub.Status == stripe.SubscriptionStatusActive && len(sub.Items.Data) > 0 {
			priceID := sub.Items.Data[0].Price.ID
			if priceID == h.cfg.StripePriceBusinessMonthly || priceID == h.cfg.StripePriceBusinessAnnual {
				plan = "business"
			} else {
				plan = "pro"
			}
		}
		_ = h.db.UpdateProfilePlan(r.Context(), profile.UserID, plan, profile.StripeCustomerID)
	default:
		// Ignore other events
	}
	w.WriteHeader(http.StatusOK)
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
	if h.cfg.IsDevBypassUser(user.ID) {
		p := *profile
		p.Plan = "business"
		profile = &p
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

	// Pro gating: logo and brand color require Pro plan
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	isPro := profile != nil && (profile.Plan == "pro" || profile.Plan == "business") || h.cfg.IsDevBypassUser(user.ID)
	if !isPro {
		curLogo, curBrand := "", ""
		if profile != nil {
			if profile.LogoURL != nil {
				curLogo = *profile.LogoURL
			}
			curBrand = profile.BrandColor
		}
		newLogo := ""
		if req.LogoURL != nil {
			newLogo = *req.LogoURL
		}
		if (req.LogoURL != nil && newLogo != curLogo) || (req.BrandColor != "" && req.BrandColor != curBrand) {
			h.json(w, http.StatusPaymentRequired, models.APIResponse{
				Success: false,
				Error:   "pro_required",
				Message: "Custom branding (logo, brand color) requires a Pro plan. Upgrade to unlock.",
			})
			return
		}
	}

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
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	client := &models.Client{
		UserID:  user.ID,
		TeamID:  profile.TeamID,
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
	unread, _ := h.db.GetQuoteIDsWithUnreadNotes(r.Context(), user.ID)
	for i := range quotes {
		quotes[i].HasUnreadNotes = unread[quotes[i].ID]
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
	// Dev bypass: in development, DEV_BYPASS_USER_ID gets unlimited access
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	isPro := profile != nil && (profile.Plan == "pro" || profile.Plan == "business") || h.cfg.IsDevBypassUser(user.ID)
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
		// Pro gating: track_views requires Pro
		if req.TrackViews {
			h.json(w, http.StatusPaymentRequired, models.APIResponse{
				Success: false,
				Error:   "pro_required",
				Message: "View tracking requires a Pro plan. Upgrade to unlock.",
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
		TeamID:           profile.TeamID,
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

// PATCH /quotes/:id — update quote fields and optionally replace line items (draft only)
func (h *Handler) UpdateQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")

	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	if quote.Status != models.StatusDraft {
		h.err(w, http.StatusBadRequest, "only draft quotes can be edited — sent and accepted quotes cannot be modified")
		return
	}

	var req models.UpdateQuoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Pro gating: track_views requires Pro
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	isPro := profile != nil && (profile.Plan == "pro" || profile.Plan == "business") || h.cfg.IsDevBypassUser(user.ID)
	if !isPro && req.TrackViews != nil && *req.TrackViews {
		h.json(w, http.StatusPaymentRequired, models.APIResponse{
			Success: false,
			Error:   "pro_required",
			Message: "View tracking requires a Pro plan. Upgrade to unlock.",
		})
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
		whiteLabel := profile != nil && profile.Plan == "business"
		if err := h.notif.SendQuoteByEmail(quote, recipient, senderName, whiteLabel); err != nil {
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
	isPro := profile != nil && (profile.Plan == "pro" || profile.Plan == "business") || h.cfg.IsDevBypassUser(user.ID)
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
		// Pro gating: cannot duplicate quote with track_views on free plan
		source, _ := h.db.GetQuote(r.Context(), id, user.ID)
		if source != nil && source.TrackViews {
			h.json(w, http.StatusPaymentRequired, models.APIResponse{
				Success: false,
				Error:   "pro_required",
				Message: "View tracking requires a Pro plan. Upgrade to duplicate quotes with view tracking.",
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
		LogoURL      *string `json:"logo_url,omitempty"`
		BusinessName string  `json:"business_name,omitempty"`
		BrandColor   string  `json:"brand_color,omitempty"`
		WhiteLabel   bool    `json:"white_label,omitempty"` // Business plan: no QuoteFlow fallback
	}
	out := struct {
		models.QuoteWithDetails
		Creator *creatorInfo `json:"creator,omitempty"`
	}{QuoteWithDetails: *quote}
	if profile != nil {
		out.Creator = &creatorInfo{
			LogoURL:      profile.LogoURL,
			BusinessName: profile.BusinessName,
			BrandColor:   profile.BrandColor,
			WhiteLabel:   profile.Plan == "business",
		}
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

// GET /q/:token/notes — public, returns notes thread for the quote
func (h *Handler) PublicGetNotes(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	notes, err := h.db.GetNotesByShareToken(r.Context(), token)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found or link has expired")
		return
	}
	h.ok(w, notes)
}

// POST /q/:token/notes — public, client posts a note (or change request)
func (h *Handler) PublicPostNote(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	quote, err := h.db.GetQuoteByShareToken(r.Context(), token)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found or link has expired")
		return
	}
	var req models.PostNoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	noteType := req.NoteType
	if noteType != "message" && noteType != "change_request" {
		noteType = "message"
	}
	note, err := h.db.AddNote(r.Context(), quote.ID, "client", req.Name, req.Message, noteType)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to add note")
		return
	}
	if noteType == "change_request" {
		_ = h.db.UpdateQuoteStatus(r.Context(), quote.ID, quote.UserID, models.StatusDraft)
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			profile, _ := h.db.GetProfile(bgCtx, quote.UserID)
			if profile != nil && profile.EmailOnQuote != "" {
				_ = h.notif.SendChangeRequestNotification(&quote.Quote, quote.Client.Name, req.Name, req.Message, profile.EmailOnQuote)
			}
		}()
	}
	h.created(w, note)
}

// GET /quotes/:id/notes — authenticated, returns notes thread
func (h *Handler) GetNotes(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	notes, err := h.db.GetNotesByQuoteID(r.Context(), quote.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load notes")
		return
	}
	h.ok(w, notes)
}

// POST /quotes/:id/notes — authenticated, freelancer replies
func (h *Handler) PostNote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	var req models.ReplyNoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	authorName := "You"
	if profile != nil && profile.BusinessName != "" {
		authorName = profile.BusinessName
	}
	note, err := h.db.AddNote(r.Context(), quote.ID, "freelancer", authorName, req.Message, "message")
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to add note")
		return
	}
	h.created(w, note)
}

// PATCH /quotes/:id/notes/read — authenticated, marks client notes as read
func (h *Handler) MarkNotesRead(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	quote, err := h.db.GetQuote(r.Context(), id, user.ID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	if err := h.db.MarkNotesAsRead(r.Context(), quote.ID); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to mark notes as read")
		return
	}
	h.ok(w, map[string]bool{"read": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE TEMPLATES
// ─────────────────────────────────────────────────────────────────────────────

// GET /templates — list user's templates
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	templates, err := h.db.ListTemplates(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load templates")
		return
	}
	h.ok(w, templates)
}

// POST /templates — create template from scratch
func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.CreateTemplateRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	tpl, err := h.db.CreateTemplate(r.Context(), user.ID, &req)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to create template")
		return
	}
	h.created(w, tpl)
}

// POST /templates/from-quote — create template from existing quote
func (h *Handler) CreateTemplateFromQuote(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.CreateTemplateFromQuoteRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}
	tpl, err := h.db.CreateTemplateFromQuote(r.Context(), user.ID, req.Name, req.QuoteID)
	if err != nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	h.created(w, tpl)
}

// DELETE /templates/:id
func (h *Handler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteTemplate(r.Context(), id, user.ID); err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}
	h.ok(w, map[string]bool{"deleted": true})
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
