package handlers

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/stripe/stripe-go/v76"
	portalsession "github.com/stripe/stripe-go/v76/billingportal/session"
	"github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
	"github.com/stripe/stripe-go/v76/webhook"
	"quoteflow-backend/config"
	"quoteflow-backend/internal/digest"
	"quoteflow-backend/internal/middleware"
	"quoteflow-backend/internal/models"
	"quoteflow-backend/internal/repository"
	"quoteflow-backend/internal/services"
)

var validate = validator.New()

// Handler holds all dependencies for the HTTP handlers.
type Handler struct {
	db       *repository.DB
	notif    *services.NotificationService
	auth     *services.AuthService
	payments *services.PaymentService
	email    *services.EmailService
	digest   *digest.Service
	cfg      *config.Config
}

func New(db *repository.DB, notif *services.NotificationService, auth *services.AuthService, payments *services.PaymentService, email *services.EmailService, digest *digest.Service, cfg *config.Config) *Handler {
	return &Handler{db: db, notif: notif, auth: auth, payments: payments, email: email, digest: digest, cfg: cfg}
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

func formatProcessor(p models.PaymentProcessor) string {
	switch p {
	case models.ProcessorWiPay:
		return "WiPay"
	case models.ProcessorStripe:
		return "Stripe"
	case models.ProcessorPayPal:
		return "PayPal"
	default:
		return string(p)
	}
}

func formatPaymentType(t models.PaymentType) string {
	switch t {
	case models.PaymentTypeDeposit:
		return "Deposit"
	case models.PaymentTypeBalance:
		return "Balance Payment"
	case models.PaymentTypeFull:
		return "Full Payment"
	default:
		return string(t)
	}
}

func formatAmount(amount float64, currency string) string {
	symbols := map[string]string{
		"JMD": "J$",
		"USD": "$",
		"TTD": "TT$",
		"BBD": "Bds$",
		"GYD": "G$",
		"GBP": "£",
		"EUR": "€",
	}
	symbol := symbols[currency]
	if symbol == "" {
		symbol = currency + " "
	}
	// Format with thousands separators
	parts := strings.Split(fmt.Sprintf("%.2f", amount), ".")
	intPart := parts[0]
	var b strings.Builder
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(",")
		}
		b.WriteByte(intPart[i])
	}
	return symbol + b.String() + "." + parts[1]
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

// POST /internal/cron/digest-weekly — sends weekly digest emails.
// Protected by CRON_SECRET. Invoke via external cron every Monday 8am Jamaica (13:00 UTC).
func (h *Handler) CronWeeklyDigest(w http.ResponseWriter, r *http.Request) {
	if h.digest == nil {
		h.err(w, http.StatusInternalServerError, "digest service not configured")
		return
	}
	if err := h.digest.SendWeeklyDigests(r.Context()); err != nil {
		h.err(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.ok(w, map[string]interface{}{"message": "weekly digest sent"})
}

// POST /admin/digest/weekly — manual trigger for testing. Requires Authorization: Bearer <ADMIN_SECRET>.
func (h *Handler) TriggerWeeklyDigest(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+h.cfg.AdminSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.digest == nil {
		http.Error(w, "digest service not configured", http.StatusInternalServerError)
		return
	}
	go h.digest.SendWeeklyDigests(context.Background())
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"message":"digest triggered"}`))
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
		h.err(w, http.StatusNotFound, "user not found — they must sign up for QuoteFlow first, then you can add them by email")
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

// GET /teams/invited — teams where user is member/admin (not owner)
func (h *Handler) GetTeamsInvited(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	teams, err := h.db.ListTeamsInvitedTo(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load invited teams")
		return
	}
	if teams == nil {
		teams = []models.Team{}
	}
	h.ok(w, teams)
}

// POST /teams/:id/sync — switch profile to this team (user must be member)
func (h *Handler) SyncTeam(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	ok, _ := h.db.IsTeamMember(r.Context(), id, user.ID)
	if !ok {
		h.err(w, http.StatusForbidden, "not a team member")
		return
	}
	if err := h.db.SyncTeam(r.Context(), id, user.ID); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to sync")
		return
	}
	team, _ := h.db.GetTeam(r.Context(), id)
	h.ok(w, team)
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
// PAYMENTS
// ─────────────────────────────────────────────────────────────────────────────

// GET /payments/accounts
func (h *Handler) ListPaymentAccounts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	accounts, err := h.db.ListPaymentAccounts(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load payment accounts")
		return
	}
	if accounts == nil {
		accounts = []models.PaymentAccount{}
	}
	h.ok(w, accounts)
}

// POST /payments/connect/stripe
func (h *Handler) ConnectStripe(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	oauthURL := fmt.Sprintf(
		"https://connect.stripe.com/oauth/authorize?response_type=code&client_id=%s&scope=read_write&state=%s",
		h.cfg.StripeClientID,
		user.ID,
	)
	h.ok(w, map[string]string{"url": oauthURL})
}

// GET /payments/connect/stripe/callback
func (h *Handler) StripeConnectCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&error=stripe", http.StatusFound)
		return
	}
	resp, err := h.payments.ExchangeStripeCode(code)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&error=stripe", http.StatusFound)
		return
	}
	if err := h.db.UpsertPaymentAccount(r.Context(), state, "stripe", map[string]string{
		"stripe_account_id":   resp.StripeUserID,
		"stripe_access_token": resp.AccessToken,
	}); err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&error=stripe", http.StatusFound)
		return
	}
	http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&connected=stripe", http.StatusFound)
}

// POST /payments/connect/paypal
func (h *Handler) ConnectPayPal(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	onboardingURL, err := h.payments.CreatePayPalOnboardingLink(user.ID)
	if err != nil {
		log.Printf("[PayPal] ConnectPayPal handler error: %v", err)
		errMsg := "failed to create PayPal onboarding link"
		if h.cfg.IsDevelopment() {
			errMsg = err.Error()
		}
		h.err(w, http.StatusInternalServerError, errMsg)
		return
	}
	h.ok(w, map[string]string{"url": onboardingURL})
}

// GET /payments/connect/paypal/callback
func (h *Handler) PayPalConnectCallback(w http.ResponseWriter, r *http.Request) {
	paypalMerchantID := r.URL.Query().Get("merchantIdInPayPal")
	trackingID := r.URL.Query().Get("merchantId")
	if paypalMerchantID == "" || trackingID == "" {
		http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&error=paypal", http.StatusFound)
		return
	}
	if err := h.db.UpsertPaymentAccount(r.Context(), trackingID, "paypal", map[string]string{
		"paypal_merchant_id": paypalMerchantID,
	}); err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&error=paypal", http.StatusFound)
		return
	}
	http.Redirect(w, r, h.cfg.FrontendURL+"/app/settings?tab=payments&connected=paypal", http.StatusFound)
}

// POST /payments/connect/wipay
func (h *Handler) ConnectWiPay(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)

	var req models.ConnectWiPayRequest
	if err := h.decode(r, &req); err != nil {
		log.Printf("[WiPay] ConnectWiPay decode failed: %v", err)
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		log.Printf("[WiPay] ConnectWiPay validation failed: %v", err)
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}

	log.Printf("[WiPay] ConnectWiPay saving account for user %s", user.ID)
	if err := h.db.SaveWiPayAccount(r.Context(), user.ID, req.AccountNumber, req.APIKey); err != nil {
		log.Printf("[WiPay] ConnectWiPay SaveWiPayAccount failed: %v", err)
		h.err(w, http.StatusInternalServerError, "failed to save WiPay account")
		return
	}

	h.ok(w, map[string]interface{}{
		"connected": true,
		"processor": "wipay",
		"message":   "WiPay connected successfully",
	})
}

// DELETE /payments/disconnect/:processor
func (h *Handler) DisconnectProcessor(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	processor := chi.URLParam(r, "processor")
	if processor != "stripe" && processor != "paypal" && processor != "wipay" {
		h.err(w, http.StatusBadRequest, "invalid processor")
		return
	}
	if err := h.db.DisconnectPaymentAccount(r.Context(), user.ID, processor); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to disconnect")
		return
	}
	h.ok(w, map[string]bool{"disconnected": true})
}

// calculatePlatformFee returns the platform fee for a transaction.
// WiPay: 0% — we cannot auto-collect fees from WiPay transactions.
// Stripe/PayPal: 0.7% — collected automatically.
func (h *Handler) calculatePlatformFee(amount float64, processor models.PaymentProcessor) float64 {
	if processor == models.ProcessorWiPay {
		return 0
	}
	return math.Round(amount*h.cfg.PlatformFeePercent*100) / 100
}

func calcDeposit(quote *models.QuoteWithDetails) float64 {
	raw := strings.TrimSuffix(strings.TrimSpace(quote.Deposit), "%")
	pct, err := strconv.ParseFloat(raw, 64)
	if err != nil || pct <= 0 || pct > 100 {
		return math.Round(quote.Total*0.5*100) / 100
	}
	return math.Round(quote.Total*(pct/100)*100) / 100
}

// POST /payments/create-link
func (h *Handler) CreatePaymentLink(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req models.CreatePaymentLinkRequest
	if err := h.decode(r, &req); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRequest(&req); err != nil {
		h.err(w, http.StatusBadRequest, validationErrorMsg(err))
		return
	}

	quote, err := h.db.GetQuote(r.Context(), req.QuoteID, user.ID)
	if err != nil || quote == nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}

	var amount float64
	switch req.PaymentType {
	case models.PaymentTypeFull:
		amount = quote.Total
	case models.PaymentTypeDeposit:
		amount = calcDeposit(quote)
	case models.PaymentTypeBalance:
		depositPaid, _ := h.db.GetDepositPaid(r.Context(), quote.ID)
		amount = math.Round((quote.Total-depositPaid)*100) / 100
		if amount <= 0 {
			h.err(w, http.StatusBadRequest, "balance already paid")
			return
		}
	}

	account, err := h.db.GetBestPaymentAccountFull(r.Context(), user.ID, quote.Currency)
	if err != nil || account == nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}

	platformFee := h.calculatePlatformFee(amount, account.Processor)
	netAmount := amount - platformFee

	var paymentURL, processorPaymentID string

	switch account.Processor {
	case models.ProcessorStripe:
		link, err := h.payments.CreateStripePaymentLink(account, quote, amount, platformFee, req.PaymentType)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "Stripe error: "+err.Error())
			return
		}
		paymentURL, processorPaymentID = link.URL, link.ID
	case models.ProcessorPayPal:
		link, err := h.payments.CreatePayPalOrder(account, quote, amount, platformFee, req.PaymentType)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "PayPal error: "+err.Error())
			return
		}
		paymentURL, processorPaymentID = link.ApproveURL, link.OrderID
	case models.ProcessorWiPay:
		// WiPay uses proxy checkout — return our checkout URL
		paymentURL = strings.TrimSuffix(h.cfg.AppURL, "/") + "/q/" + quote.ShareToken + "/wipay-checkout?type=" + string(req.PaymentType)
		processorPaymentID = quote.ShareToken
	default:
		h.err(w, http.StatusBadRequest, "unsupported processor")
		return
	}

	// WiPay: payment created when client visits checkout URL — don't create here
	if account.Processor != models.ProcessorWiPay {
		payment := &models.Payment{
			QuoteID:            quote.ID,
			UserID:             user.ID,
			Processor:          account.Processor,
			PaymentType:        req.PaymentType,
			Amount:             amount,
			PlatformFee:        platformFee,
			NetAmount:          netAmount,
			Currency:           quote.Currency,
			Status:             models.PaymentStatusPending,
			ProcessorPaymentID: processorPaymentID,
			PaymentURL:         paymentURL,
		}
		if err := h.db.CreatePayment(r.Context(), payment); err != nil {
			h.err(w, http.StatusInternalServerError, "failed to create payment record")
			return
		}
	}

	h.ok(w, models.PaymentLinkResponse{
		PaymentURL:  paymentURL,
		Amount:      amount,
		PlatformFee: platformFee,
		NetAmount:   netAmount,
		Currency:    quote.Currency,
		PaymentType: req.PaymentType,
		Processor:   account.Processor,
	})
}

// GET /payments/history
func (h *Handler) ListPayments(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	payments, err := h.db.ListPayments(r.Context(), user.ID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to load payments")
		return
	}
	if payments == nil {
		payments = []models.Payment{}
	}
	h.ok(w, payments)
}

// POST /q/:token/pay — public, client creates payment link
func (h *Handler) PublicCreatePaymentLink(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	quote, err := h.db.GetQuoteByShareToken(r.Context(), token)
	if err != nil || quote == nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	if quote.Status != models.StatusAccepted {
		h.err(w, http.StatusBadRequest, "quote must be accepted before payment")
		return
	}

	var req struct {
		PaymentType models.PaymentType `json:"payment_type"`
		Processor   string            `json:"processor"` // optional: stripe, paypal — client picks when multiple available
	}
	if err := h.decode(r, &req); err != nil || req.PaymentType == "" {
		h.err(w, http.StatusBadRequest, "payment_type required")
		return
	}
	if req.PaymentType != models.PaymentTypeFull && req.PaymentType != models.PaymentTypeDeposit && req.PaymentType != models.PaymentTypeBalance {
		h.err(w, http.StatusBadRequest, "payment_type must be full, deposit, or balance")
		return
	}

	userID := quote.UserID

	var account *models.PaymentAccountFull
	if req.Processor != "" {
		proc := models.PaymentProcessor(req.Processor)
		if proc != models.ProcessorStripe && proc != models.ProcessorPayPal && proc != models.ProcessorWiPay {
			h.err(w, http.StatusBadRequest, "invalid processor")
			return
		}
		account, err = h.db.GetPaymentAccountFull(r.Context(), userID, req.Processor)
		if err != nil || account == nil {
			h.err(w, http.StatusBadRequest, "processor not connected")
			return
		}
		// Currency checks
		if proc == models.ProcessorPayPal && quote.Currency != "USD" {
			h.err(w, http.StatusBadRequest, "PayPal only supports USD")
			return
		}
		if proc == models.ProcessorWiPay && quote.Currency != "JMD" && quote.Currency != "TTD" && quote.Currency != "BBD" && quote.Currency != "GYD" {
			h.err(w, http.StatusBadRequest, "WiPay only supports JMD, TTD, BBD, and GYD")
			return
		}
	} else {
		account, err = h.db.GetBestPaymentAccountFull(r.Context(), userID, quote.Currency)
		if err != nil || account == nil {
			h.err(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var amount float64
	switch req.PaymentType {
	case models.PaymentTypeFull:
		amount = quote.Total
	case models.PaymentTypeDeposit:
		amount = calcDeposit(quote)
	case models.PaymentTypeBalance:
		depositPaid, _ := h.db.GetDepositPaid(r.Context(), quote.ID)
		amount = math.Round((quote.Total-depositPaid)*100) / 100
		if amount <= 0 {
			h.err(w, http.StatusBadRequest, "balance already paid")
			return
		}
	}

	platformFee := h.calculatePlatformFee(amount, account.Processor)
	netAmount := amount - platformFee

	var paymentURL, processorPaymentID string

	switch account.Processor {
	case models.ProcessorStripe:
		link, err := h.payments.CreateStripePaymentLink(account, quote, amount, platformFee, req.PaymentType)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "payment link failed")
			return
		}
		paymentURL, processorPaymentID = link.URL, link.ID
	case models.ProcessorPayPal:
		link, err := h.payments.CreatePayPalOrder(account, quote, amount, platformFee, req.PaymentType)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "payment link failed")
			return
		}
		paymentURL, processorPaymentID = link.ApproveURL, link.OrderID
	case models.ProcessorWiPay:
		// WiPay uses proxy checkout — return our checkout URL (payment created when they visit)
		paymentURL = strings.TrimSuffix(h.cfg.AppURL, "/") + "/q/" + quote.ShareToken + "/wipay-checkout?type=" + string(req.PaymentType)
		processorPaymentID = quote.ShareToken
	default:
		h.err(w, http.StatusBadRequest, "unsupported processor")
		return
	}

	// WiPay: payment is created when client visits checkout URL — don't create here
	if account.Processor != models.ProcessorWiPay {
		payment := &models.Payment{
			QuoteID:            quote.ID,
			UserID:             userID,
			Processor:          account.Processor,
			PaymentType:        req.PaymentType,
			Amount:             amount,
			PlatformFee:        platformFee,
			NetAmount:          netAmount,
			Currency:           quote.Currency,
			Status:             models.PaymentStatusPending,
			ProcessorPaymentID: processorPaymentID,
			PaymentURL:         paymentURL,
		}
		if err := h.db.CreatePayment(r.Context(), payment); err != nil {
			h.err(w, http.StatusInternalServerError, "failed to create payment record")
			return
		}
	}

	h.ok(w, models.PaymentLinkResponse{
		PaymentURL:  paymentURL,
		Amount:      amount,
		PlatformFee: platformFee,
		NetAmount:   netAmount,
		Currency:    quote.Currency,
		PaymentType: req.PaymentType,
		Processor:   account.Processor,
	})
}

// GET /q/:token/wipay-checkout?type=full|deposit|balance — public
// Returns an auto-submitting HTML form that POSTs directly to WiPay.
// The browser submits to WiPay's domain — no CORS issues.
func (h *Handler) WiPayCheckout(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	paymentTypeStr := r.URL.Query().Get("type")
	if paymentTypeStr == "" {
		paymentTypeStr = "full"
	}

	quote, err := h.db.GetQuoteByShareToken(r.Context(), token)
	if err != nil || quote == nil {
		h.err(w, http.StatusNotFound, "quote not found")
		return
	}
	if quote.Status != models.StatusAccepted {
		h.err(w, http.StatusBadRequest, "quote must be accepted before payment")
		return
	}
	if quote.Currency != "JMD" && quote.Currency != "TTD" && quote.Currency != "BBD" && quote.Currency != "GYD" {
		h.err(w, http.StatusBadRequest, "WiPay only supports JMD, TTD, BBD, and GYD")
		return
	}

	paymentType := models.PaymentType(paymentTypeStr)
	if paymentType != models.PaymentTypeFull && paymentType != models.PaymentTypeDeposit && paymentType != models.PaymentTypeBalance {
		h.err(w, http.StatusBadRequest, "type must be full, deposit, or balance")
		return
	}

	var amount float64
	switch paymentType {
	case models.PaymentTypeFull:
		amount = quote.Total
	case models.PaymentTypeDeposit:
		amount = calcDeposit(quote)
	case models.PaymentTypeBalance:
		depositPaid, _ := h.db.GetDepositPaid(r.Context(), quote.ID)
		amount = math.Round((quote.Total-depositPaid)*100) / 100
		if amount <= 0 {
			h.err(w, http.StatusBadRequest, "balance already paid")
			return
		}
	}

	account, err := h.db.GetPaymentAccountFull(r.Context(), quote.UserID, "wipay")
	if err != nil || account == nil || !account.IsActive {
		h.err(w, http.StatusBadRequest, "WiPay not connected")
		return
	}

	// WiPay rejects order_id with underscores/dashes — store sanitized for webhook lookup
	wipayOrderID := strings.NewReplacer("_", "", "-", "").Replace(token)

	payment := &models.Payment{
		QuoteID:            quote.ID,
		UserID:             quote.UserID,
		Processor:          models.ProcessorWiPay,
		PaymentType:        paymentType,
		Amount:             amount,
		PlatformFee:        0,
		NetAmount:          amount,
		Currency:           quote.Currency,
		Status:             models.PaymentStatusPending,
		ProcessorPaymentID: wipayOrderID,
	}
	_ = h.db.CreatePayment(r.Context(), payment)

	formData, err := h.payments.GetWiPayFormData(account, amount, quote.Currency, token)
	if err != nil {
		log.Printf("[WiPay] WiPayCheckout GetWiPayFormData failed: %v", err)
		h.err(w, http.StatusInternalServerError, "failed to prepare WiPay checkout")
		return
	}

	// Return an auto-submitting HTML form that POSTs directly to WiPay.
	// This bypasses all CORS issues — the browser submits directly to WiPay's domain.
	// Form opens WiPay in new tab so this page stays open; meta refresh returns to quote after 30s.
	fallbackURL := strings.TrimSuffix(h.cfg.FrontendURL, "/") + "/q/" + token
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>Opening WiPay...</title>
    <meta http-equiv="refresh" content="30;url=%s">
    <style>
        body { 
            font-family: -apple-system, sans-serif; 
            display: flex; 
            justify-content: center; 
            align-items: center; 
            height: 100vh; 
            margin: 0;
            background: #f5f5f5;
        }
        .box {
            text-align: center;
            background: white;
            padding: 40px;
            border-radius: 12px;
            box-shadow: 0 2px 20px rgba(0,0,0,0.08);
        }
        p { color: #666; margin-top: 12px; }
    </style>
</head>
<body>
    <div class="box">
        <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="#E85C2F" stroke-width="2">
            <circle cx="12" cy="12" r="10"/>
            <path d="M12 6v6l4 2"/>
        </svg>
        <p>Opening WiPay secure checkout...</p>
        <p style="font-size:13px;color:#666;margin-top:8px;">
            Complete your payment in the new tab.<br>
            This page will automatically update when payment is confirmed.
        </p>
        <p style="font-size:11px;color:#999;margin-top:16px;">
            If the new tab didn't open,
            <a href="#" onclick="document.getElementById('wipay-form').submit(); return false;">click here</a>
        </p>
    </div>
    <form id="wipay-form" method="POST" action="%s" target="_blank">
        <input type="hidden" name="account_number" value="%s">
        <input type="hidden" name="avs" value="%s">
        <input type="hidden" name="country_code" value="%s">
        <input type="hidden" name="currency" value="%s">
        <input type="hidden" name="environment" value="%s">
        <input type="hidden" name="fee_structure" value="%s">
        <input type="hidden" name="method" value="%s">
        <input type="hidden" name="order_id" value="%s">
        <input type="hidden" name="origin" value="%s">
        <input type="hidden" name="response_url" value="%s">
        <input type="hidden" name="return_url" value="%s">
        <input type="hidden" name="total" value="%s">
    </form>
    <script>
        document.getElementById("wipay-form").submit();
    </script>
</body>
</html>`,
		fallbackURL,
		formData.Endpoint,
		html.EscapeString(formData.AccountNumber),
		html.EscapeString(formData.AVS),
		html.EscapeString(formData.CountryCode),
		html.EscapeString(formData.Currency),
		html.EscapeString(formData.Environment),
		html.EscapeString(formData.FeeStructure),
		html.EscapeString(formData.Method),
		html.EscapeString(formData.OrderID),
		html.EscapeString(formData.Origin),
		html.EscapeString(formData.ResponseURL),
		html.EscapeString(formData.ReturnURL),
		html.EscapeString(formData.Total),
	)

	// Set cookie so /app can redirect to correct quote when WiPay sends user back
	http.SetCookie(w, &http.Cookie{
		Name:     "wipay_quote_token",
		Value:    token,
		Path:     "/",
		MaxAge:   600,
		SameSite: http.SameSiteNoneMode,
		Secure:   true,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
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
		Currency:          stripe.String("usd"),
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

// POST /billing/portal — create Stripe Customer Portal session (manage subscription, payment methods)
func (h *Handler) CreateBillingPortalSession(w http.ResponseWriter, r *http.Request) {
	if h.cfg.StripeSecretKey == "" {
		h.err(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	user := currentUser(r)
	profile, _ := h.db.GetProfile(r.Context(), user.ID)
	if profile == nil || profile.StripeCustomerID == "" {
		h.err(w, http.StatusBadRequest, "no billing account — subscribe to a plan first")
		return
	}
	stripe.Key = h.cfg.StripeSecretKey
	returnURL := strings.TrimSuffix(h.cfg.FrontendURL, "/") + "/app/settings?panel=billing"
	sess, err := portalsession.New(&stripe.BillingPortalSessionParams{
		Customer:  stripe.String(profile.StripeCustomerID),
		ReturnURL: stripe.String(returnURL),
	})
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to create portal session")
		return
	}
	h.ok(w, map[string]string{"url": sess.URL})
}

// POST /webhooks/stripe-payment — Stripe Connect payment webhook (checkout.session.completed)
func (h *Handler) StripePaymentWebhook(w http.ResponseWriter, r *http.Request) {
	if h.cfg.StripePaymentWebhookSecret == "" {
		log.Printf("[webhook] stripe-payment: STRIPE_PAYMENT_WEBHOOK_SECRET not set — webhook ignored")
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[webhook] stripe-payment: failed to read body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	opts := webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true, // Event Destination uses 2026-02-25; stripe-go expects 2023-10-16
	}
	event, err := webhook.ConstructEventWithOptions(body, sig, h.cfg.StripePaymentWebhookSecret, opts)
	if err != nil {
		log.Printf("[webhook] stripe-payment: signature verification failed")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if event.Type == "checkout.session.completed" {
		var sess stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
			log.Printf("[webhook] stripe-payment: failed to unmarshal session")
			w.WriteHeader(http.StatusOK)
			return
		}
		quoteID := sess.Metadata["quote_id"]
		paymentType := models.PaymentType(sess.Metadata["payment_type"])

		// Fallback: Payment Links on Connect may not pass metadata to session — look up by payment_link ID
		paymentLinkID := ""
		if sess.PaymentLink != nil && sess.PaymentLink.ID != "" {
			paymentLinkID = sess.PaymentLink.ID
		} else {
			var raw struct {
				PaymentLink *struct {
					ID string `json:"id"`
				} `json:"payment_link"`
			}
			if json.Unmarshal(event.Data.Raw, &raw) == nil && raw.PaymentLink != nil && raw.PaymentLink.ID != "" {
				paymentLinkID = raw.PaymentLink.ID
			} else {
				var rawStr struct {
					PaymentLink string `json:"payment_link"`
				}
				if json.Unmarshal(event.Data.Raw, &rawStr) == nil && rawStr.PaymentLink != "" {
					paymentLinkID = rawStr.PaymentLink
				}
			}
		}
		if quoteID == "" && paymentLinkID != "" {
			payment, err := h.db.GetPaymentByProcessorID(r.Context(), paymentLinkID, "stripe")
			if err == nil && payment != nil {
				quoteID = payment.QuoteID
				paymentType = payment.PaymentType
			}
		}
		if quoteID != "" && (paymentType == models.PaymentTypeFull || paymentType == models.PaymentTypeDeposit || paymentType == models.PaymentTypeBalance) {
			h.handlePaymentConfirmed(r.Context(), quoteID, paymentType)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// POST /webhooks/paypal — PayPal payment webhook
func (h *Handler) PayPalWebhook(w http.ResponseWriter, r *http.Request) {
	// Step 1: Read raw body (required for signature verification)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.err(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Step 2: Verify signature with PayPal (skip if webhook not configured)
	if h.cfg.PayPalWebhookID != "" {
		if err := h.verifyPayPalWebhook(r, body); err != nil {
			log.Printf("PayPal webhook signature verification failed: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	// Step 3: Process event
	var event struct {
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if event.EventType == "PAYMENT.CAPTURE.COMPLETED" {
		var capture struct {
			SupplementaryData struct {
				RelatedIDs struct {
					OrderID string `json:"order_id"`
				} `json:"related_ids"`
			} `json:"supplementary_data"`
		}
		_ = json.Unmarshal(event.Resource, &capture)
		orderID := capture.SupplementaryData.RelatedIDs.OrderID

		payment, err := h.db.GetPaymentByProcessorID(r.Context(), orderID, "paypal")
		if err == nil && payment != nil {
			h.handlePaymentConfirmed(r.Context(), payment.QuoteID, payment.PaymentType)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// verifyWiPayHash verifies the MD5 hash WiPay sends in the webhook response.
// WiPay docs: message = MD5(transaction_id + api_key)
func verifyWiPayHash(transactionID, apiKey, receivedHash string) bool {
	hash := md5.Sum([]byte(transactionID + apiKey))
	expected := fmt.Sprintf("%x", hash)
	return strings.EqualFold(expected, receivedHash)
}

// GET /app — WiPay redirects here after payment (ignores return_url).
// Read cookie set in WiPayCheckout to know which quote to redirect to.
func (h *Handler) WiPayAppRedirect(w http.ResponseWriter, r *http.Request) {
	log.Printf("[WiPay] /app hit: url=%s query=%s",
		r.URL.String(), r.URL.RawQuery)

	cookie, err := r.Cookie("wipay_quote_token")
	if err == nil && cookie.Value != "" {
		// Clear the cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "wipay_quote_token",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			SameSite: http.SameSiteNoneMode,
			Secure:   true,
		})
		redirectURL := fmt.Sprintf("%s/q/%s?payment=success&processor=wipay",
			strings.TrimSuffix(h.cfg.FrontendURL, "/"), cookie.Value)
		log.Printf("[WiPay] /app redirecting to: %s", redirectURL)
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
	// Fallback — redirect to frontend home
	redirectURL := strings.TrimSuffix(h.cfg.FrontendURL, "/")
	log.Printf("[WiPay] /app no cookie, redirecting to: %s", redirectURL)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// GET /webhooks/wipay — WiPay sends webhook as GET with query string parameters.
// Public endpoint — no JWT. Verified via MD5 hash instead.
func (h *Handler) WiPayWebhook(w http.ResponseWriter, r *http.Request) {
	// Step 1 — Extract params
	status := r.URL.Query().Get("status")
	orderID := r.URL.Query().Get("order_id")
	transactionID := r.URL.Query().Get("transaction_id")
	hash := r.URL.Query().Get("hash")
	totalStr := r.URL.Query().Get("total")
	accountNumber := r.URL.Query().Get("account_number")

	log.Printf("[WiPay] webhook received: status=%s order_id=%s transaction_id=%s total=%s",
		status, orderID, transactionID, totalStr)

	if status != "success" {
		log.Printf("[WiPay] webhook: non-success status=%s — ignoring", status)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Step 2 — HASH VERIFICATION FIRST (before any DB lookup) — production only
	if !h.cfg.WiPaySandbox {
		if transactionID == "" || hash == "" {
			log.Printf("[WiPay] webhook: missing transaction_id or hash — rejected")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if accountNumber == "" {
			log.Printf("[WiPay] webhook: missing account_number — rejected")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		account, err := h.db.GetWiPayAccountByNumber(r.Context(), accountNumber)
		if err != nil {
			log.Printf("[WiPay] webhook: account not found for %s — rejected: %v", accountNumber, err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		expectedHash := fmt.Sprintf("%x", md5.Sum([]byte(transactionID+account.WiPayAPIKey)))
		if !strings.EqualFold(hash, expectedHash) {
			log.Printf("[WiPay] webhook: hash mismatch REJECTED got=%s expected=%s",
				hash, expectedHash)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		log.Printf("[WiPay] webhook: hash verified ok")

		// Step 3 — AMOUNT VALIDATION (parse only; DB amount check after lookup)
		if _, err := strconv.ParseFloat(totalStr, 64); err != nil {
			log.Printf("[WiPay] webhook: invalid amount total=%q — rejected", totalStr)
			http.Error(w, "invalid amount", http.StatusBadRequest)
			return
		}
	}

	// Step 4 — Look up payment (only after hash verified in production)
	if orderID == "" {
		log.Printf("[WiPay] webhook: missing order_id")
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	payment, err := h.db.GetPaymentByProcessorID(r.Context(), orderID, "wipay")
	if err != nil || payment == nil {
		log.Printf("[WiPay] webhook: payment not found for order_id=%s: %v", orderID, err)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Step 5 — Amount verification (prevent lower-amount confirmation)
	webhookAmount, err := strconv.ParseFloat(totalStr, 64)
	if err != nil {
		log.Printf("[WiPay] webhook: invalid amount in webhook total=%q", totalStr)
		http.Error(w, "invalid amount", http.StatusBadRequest)
		return
	}
	if math.Abs(webhookAmount-payment.Amount) > 0.01 {
		log.Printf("[WiPay] webhook: amount mismatch webhook=%.2f db=%.2f",
			webhookAmount, payment.Amount)
		if !h.cfg.WiPaySandbox {
			http.Error(w, "amount mismatch", http.StatusBadRequest)
			return
		}
	}

	if transactionID != "" {
		_ = h.db.UpdatePaymentProcessorID(r.Context(), payment.ID, transactionID)
	}

	h.handlePaymentConfirmed(r.Context(), payment.QuoteID, payment.PaymentType)
	log.Printf("[WiPay] webhook: payment confirmed for order_id=%s", orderID)
	w.WriteHeader(http.StatusOK)
}

// verifyPayPalWebhook calls PayPal's verification API to confirm the webhook is genuine.
func (h *Handler) verifyPayPalWebhook(r *http.Request, body []byte) error {
	accessToken, err := h.payments.GetPayPalPlatformToken()
	if err != nil {
		return fmt.Errorf("failed to get PayPal token: %w", err)
	}

	verifyPayload := map[string]interface{}{
		"auth_algo":         r.Header.Get("PAYPAL-AUTH-ALGO"),
		"cert_url":          r.Header.Get("PAYPAL-CERT-URL"),
		"transmission_id":   r.Header.Get("PAYPAL-TRANSMISSION-ID"),
		"transmission_sig":  r.Header.Get("PAYPAL-TRANSMISSION-SIG"),
		"transmission_time": r.Header.Get("PAYPAL-TRANSMISSION-TIME"),
		"webhook_id":        h.cfg.PayPalWebhookID,
		"webhook_event":     json.RawMessage(body),
	}

	payloadBytes, _ := json.Marshal(verifyPayload)
	baseURL := "https://api.paypal.com"
	if strings.ToLower(h.cfg.PayPalEnvironment) == "sandbox" {
		baseURL = "https://api.sandbox.paypal.com"
	}
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		baseURL+"/v1/notifications/verify-webhook-signature",
		bytes.NewReader(payloadBytes))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PayPal verify request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		VerificationStatus string `json:"verification_status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)

	if result.VerificationStatus != "SUCCESS" {
		return fmt.Errorf("PayPal verification status: %s", result.VerificationStatus)
	}
	return nil
}

// handlePaymentConfirmed is called by payment webhooks when payment succeeds.
func (h *Handler) handlePaymentConfirmed(ctx context.Context, quoteID string, paymentType models.PaymentType) {
	payment, err := h.db.GetPaymentByQuoteAndType(ctx, quoteID, paymentType)
	if err != nil || payment == nil {
		return
	}
	_ = h.db.MarkPaymentPaid(ctx, payment.ID)
	_ = h.db.UpdateQuotePaymentStatus(ctx, payment.QuoteID, payment.PaymentType)
	_ = h.db.LogEvent(ctx, payment.UserID, payment.QuoteID, "paid",
		fmt.Sprintf("Payment received via %s", payment.Processor))

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		quote, _ := h.db.GetQuoteByID(bgCtx, payment.QuoteID)
		profile, _ := h.db.GetProfile(bgCtx, payment.UserID)
		if quote != nil && profile != nil {
			_ = h.notif.SendPaymentReceivedNotification(quote, payment, profile.EmailOnQuote)
		}
		// Send receipt email to client
		if quote != nil && quote.Client.Email != "" {
			freelancerName := "Your service provider"
			businessName := ""
			if profile != nil {
				businessName = profile.BusinessName
				if profile.BusinessName != "" {
					freelancerName = profile.BusinessName
				} else if profile.Profession != "" {
					freelancerName = profile.Profession
				}
			}
			whiteLabel := profile != nil && profile.Plan == "business"
			receiptData := services.PaymentReceiptData{
				ClientName:     quote.Client.Name,
				ClientEmail:    quote.Client.Email,
				FreelancerName: freelancerName,
				BusinessName:   businessName,
				ReceiptNumber:  fmt.Sprintf("QF-RCP-%s", quote.QuoteNumber),
				TransactionID:  payment.ProcessorPaymentID,
				Processor:      formatProcessor(payment.Processor),
				PaymentDate:    time.Now().Format("January 2, 2006"),
				PaymentType:    formatPaymentType(payment.PaymentType),
				QuoteNumber:    quote.QuoteNumber,
				QuoteURL:       fmt.Sprintf("%s/q/%s", strings.TrimSuffix(h.cfg.FrontendURL, "/"), quote.ShareToken),
				WhiteLabel:     whiteLabel,
				Amount:         formatAmount(payment.Amount, payment.Currency),
				Currency:       payment.Currency,
			}
			if err := h.email.SendPaymentReceiptEmail(receiptData); err != nil {
				log.Printf("[Receipt] failed to send receipt email: %v", err)
			}
		}
	}()
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
	opts := webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true, // Stripe Dashboard sends 2026-02-25; stripe-go expects 2023-10-16
	}
	event, err := webhook.ConstructEventWithOptions(body, sig, h.cfg.StripeWebhookSecret, opts)
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
	// Proactive check: client with quotes cannot be deleted (FK restrict)
	count, err := h.db.CountQuotesForClient(r.Context(), id)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to check client")
		return
	}
	if count > 0 {
		h.err(w, http.StatusConflict, "cannot delete client with existing quotes — delete or reassign their quotes first")
		return
	}
	if err := h.db.DeleteClient(r.Context(), id, user.ID); err != nil {
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
		quoteID := quote.ID
		userID := quote.UserID
		clientName := quote.Client.Name
		quoteNumber := quote.QuoteNumber
		viewCount := quote.ViewCount
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = h.db.IncrementViewCount(bgCtx, quoteID)
			if profile, err := h.db.GetProfile(bgCtx, userID); err == nil {
				_ = h.notif.SendQuoteViewedNotification(&models.Quote{QuoteNumber: quoteNumber, ViewCount: viewCount}, clientName, profile.EmailOnQuote)
			}
		}()
	}

	// Include creator profile (logo, business name) for display on public quote
	profile, _ := h.db.GetProfile(r.Context(), quote.UserID)
	paymentProcessors := h.db.ListPaymentProcessorsForCurrency(r.Context(), quote.UserID, quote.Currency)
	defaultPaymentTiming := "link_only"
	if profile != nil && profile.DefaultPaymentTiming != "" {
		defaultPaymentTiming = profile.DefaultPaymentTiming
	}
	type creatorInfo struct {
		LogoURL              *string `json:"logo_url,omitempty"`
		BusinessName         string  `json:"business_name,omitempty"`
		BrandColor           string  `json:"brand_color,omitempty"`
		WhiteLabel           bool    `json:"white_label,omitempty"`
		FreelancerPlan       string  `json:"freelancer_plan,omitempty"`
		DefaultPaymentTiming string  `json:"default_payment_timing,omitempty"`
	}
	out := struct {
		models.QuoteWithDetails
		Creator           *creatorInfo `json:"creator,omitempty"`
		PaymentProcessors []string    `json:"payment_processors,omitempty"`
	}{QuoteWithDetails: *quote, PaymentProcessors: paymentProcessors}
	if profile != nil {
		plan := "free"
		if profile.Plan != "" {
			plan = profile.Plan
		}
		out.Creator = &creatorInfo{
			LogoURL:              profile.LogoURL,
			BusinessName:         profile.BusinessName,
			BrandColor:           profile.BrandColor,
			WhiteLabel:           plan == "business",
			FreelancerPlan:       plan,
			DefaultPaymentTiming: defaultPaymentTiming,
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

	quoteID := quote.ID
	userID := quote.UserID
	quoteNumber := quote.QuoteNumber
	acceptedByName := quote.AcceptedByName
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		fullQuote, err := h.db.GetQuote(bgCtx, quoteID, userID)
		if err != nil {
			return
		}
		profile, err := h.db.GetProfile(bgCtx, userID)
		if err != nil {
			return
		}
		_ = h.notif.SendQuoteAcceptedNotification(fullQuote, profile.EmailOnQuote)
		signer := acceptedByName
		if signer == "" && fullQuote.Client.Name != "" {
			signer = fullQuote.Client.Name
		}
		if signer == "" {
			signer = "Client"
		}
		_ = h.db.LogEvent(bgCtx, userID, quoteID, "accepted",
			fmt.Sprintf("%s accepted quote %s", signer, quoteNumber))
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
