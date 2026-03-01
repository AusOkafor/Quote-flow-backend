package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	postgrest "github.com/supabase-community/postgrest-go"
	supa "github.com/supabase-community/supabase-go"
	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
)

// DB wraps the Supabase client and exposes all data access methods.
type DB struct {
	client *supa.Client
	cfg    *config.Config
}

// New creates a new DB repository using the Supabase service role key
// (bypasses RLS — use only on the server, never expose to clients).
func New(cfg *config.Config) (*DB, error) {
	client, err := supa.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey, &supa.ClientOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating supabase client: %w", err)
	}
	return &DB{client: client, cfg: cfg}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HELPER
// ─────────────────────────────────────────────────────────────────────────────

func decode(raw []byte, dest interface{}) error {
	return json.Unmarshal(raw, dest)
}

func defaultIfEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// userOrTeamFilter returns (teamID, "") if user has team, else ("", userID) for filtering.
func (db *DB) userOrTeamFilter(ctx context.Context, userID string) (teamID, uid string) {
	profile, _ := db.GetProfile(ctx, userID)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		return *profile.TeamID, ""
	}
	return "", userID
}

// ─────────────────────────────────────────────────────────────────────────────
// PROFILE
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) GetProfile(ctx context.Context, userID string) (*models.Profile, error) {
	raw, _, err := db.client.From("profiles").
		Select("*", "exact", false).
		Eq("user_id", userID).
		Single().
		Execute()
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	var p models.Profile
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (db *DB) UpsertProfile(ctx context.Context, p *models.Profile) error {
	p.UpdatedAt = time.Now()
	row := map[string]interface{}{
		"user_id":              p.UserID,
		"business_name":        p.BusinessName,
		"profession":           p.Profession,
		"address":              p.Address,
		"phone":                p.Phone,
		"email_on_quote":       p.EmailOnQuote,
		"brand_color":           p.BrandColor,
		"default_currency":     p.DefaultCurrency,
		"default_validity_days": p.DefaultValidity,
		"default_deposit":      p.DefaultDeposit,
		"default_revisions":    p.DefaultRevisions,
		"default_notes":        p.DefaultNotes,
		"default_payment":      p.DefaultPayment,
		"tax_type":             p.TaxType,
		"tax_rate":             p.TaxRate,
		"tax_number":           p.TaxNumber,
		"tax_exempt_default":   p.TaxExemptDefault,
		"show_tax_breakdown":   p.ShowTaxBreakdown,
		"plan":                 defaultIfEmpty(p.Plan, "free"),
		"notify_accepted":      p.NotifyAccepted,
		"notify_viewed":        p.NotifyViewed,
		"notify_expiring":      p.NotifyExpiring,
		"notify_weekly":        p.NotifyWeekly,
		"updated_at":           p.UpdatedAt,
	}
	if p.LogoURL != nil && *p.LogoURL != "" {
		row["logo_url"] = *p.LogoURL
	} else {
		row["logo_url"] = nil
	}
	if p.StripeCustomerID != "" {
		row["stripe_customer_id"] = p.StripeCustomerID
	}
	if p.TeamID != nil {
		row["team_id"] = *p.TeamID
	}
	raw, _, err := db.client.From("profiles").
		Upsert(row, "user_id", "representation", "").
		Execute()
	if err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}
	var results []models.Profile
	if err := decode(raw, &results); err != nil {
		return err
	}
	if len(results) > 0 {
		*p = results[0]
	}
	return nil
}

// UpdateProfilePlan updates plan and stripe_customer_id for a user (used by Stripe webhooks).
func (db *DB) UpdateProfilePlan(ctx context.Context, userID, plan, stripeCustomerID string) error {
	row := map[string]interface{}{
		"plan":                 plan,
		"updated_at":           time.Now(),
	}
	if stripeCustomerID != "" {
		row["stripe_customer_id"] = stripeCustomerID
	}
	_, _, err := db.client.From("profiles").
		Update(row, "*", "").
		Eq("user_id", userID).
		Execute()
	return err
}

// GetProfileByStripeCustomerID returns the profile for a Stripe customer ID.
func (db *DB) GetProfileByStripeCustomerID(ctx context.Context, stripeCustomerID string) (*models.Profile, error) {
	raw, _, err := db.client.From("profiles").
		Select("*", "exact", false).
		Eq("stripe_customer_id", stripeCustomerID).
		Single().
		Execute()
	if err != nil {
		return nil, err
	}
	var p models.Profile
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CLIENTS
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) ListClients(ctx context.Context, userID string) ([]models.Client, error) {
	profile, _ := db.GetProfile(ctx, userID)
	q := db.client.From("client_summary").Select("*", "exact", false)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		q = q.Eq("team_id", *profile.TeamID)
	} else {
		q = q.Eq("user_id", userID)
	}
	raw, _, err := q.Order("created_at", &postgrest.OrderOpts{Ascending: false}).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	var clients []models.Client
	if err := decode(raw, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

func (db *DB) GetClient(ctx context.Context, id, userID string) (*models.Client, error) {
	profile, _ := db.GetProfile(ctx, userID)
	q := db.client.From("client_summary").Select("*", "exact", false).Eq("id", id)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		q = q.Eq("team_id", *profile.TeamID)
	} else {
		q = q.Eq("user_id", userID)
	}
	raw, _, err := q.Single().
		Execute()
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}
	var c models.Client
	return &c, decode(raw, &c)
}

func (db *DB) CreateClient(ctx context.Context, c *models.Client) error {
	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now
	row := map[string]interface{}{
		"user_id":    c.UserID,
		"name":       c.Name,
		"company":    c.Company,
		"email":      c.Email,
		"phone":      c.Phone,
		"address":    c.Address,
		"notes":      c.Notes,
		"created_at": now,
		"updated_at": now,
	}
	if c.TeamID != nil && *c.TeamID != "" {
		row["team_id"] = *c.TeamID
	}
	raw, _, err := db.client.From("clients").
		Insert(row, false, "", "representation", "").
		Execute()
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	var results []models.Client
	if err := decode(raw, &results); err != nil {
		return err
	}
	if len(results) > 0 {
		*c = results[0]
	}
	return nil
}

func (db *DB) UpdateClient(ctx context.Context, c *models.Client) error {
	c.UpdatedAt = time.Now()
	q := db.client.From("clients").Update(c, "*", "").Eq("id", c.ID)
	if c.TeamID != nil && *c.TeamID != "" {
		q = q.Eq("team_id", *c.TeamID)
	} else {
		q = q.Eq("user_id", c.UserID)
	}
	_, _, err := q.Execute()
	return err
}

func (db *DB) DeleteClient(ctx context.Context, id, userID string) error {
	profile, _ := db.GetProfile(ctx, userID)
	q := db.client.From("clients").Delete("*", "").Eq("id", id)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		q = q.Eq("team_id", *profile.TeamID)
	} else {
		q = q.Eq("user_id", userID)
	}
	_, _, err := q.Execute()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTES
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) ListQuotes(ctx context.Context, userID string, statusFilter string, currencyFilter string) ([]models.Quote, error) {
	profile, _ := db.GetProfile(ctx, userID)
	base := db.client.From("quotes").Select("*,client:clients(*)", "exact", false)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		base = base.Eq("team_id", *profile.TeamID)
	} else {
		base = base.Eq("user_id", userID)
	}
	q := base.Order("created_at", &postgrest.OrderOpts{Ascending: false})

	if statusFilter != "" && statusFilter != "all" {
		q = q.Eq("status", statusFilter)
	}
	if currencyFilter != "" {
		q = q.Eq("currency", currencyFilter)
	}

	raw, _, err := q.Execute()
	if err != nil {
		return nil, fmt.Errorf("list quotes: %w", err)
	}

	var quotes []models.Quote
	return quotes, decode(raw, &quotes)
}

func (db *DB) GetQuote(ctx context.Context, id, userID string) (*models.QuoteWithDetails, error) {
	profile, _ := db.GetProfile(ctx, userID)
	query := db.client.From("quotes").Select("*,client:clients(*),line_items(*)", "exact", false).Eq("id", id)
	if profile != nil && profile.TeamID != nil && *profile.TeamID != "" {
		query = query.Eq("team_id", *profile.TeamID)
	} else {
		query = query.Eq("user_id", userID)
	}
	raw, _, err := query.Single().
		Execute()
	if err != nil {
		return nil, fmt.Errorf("get quote: %w", err)
	}
	var result models.QuoteWithDetails
	return &result, decode(raw, &result)
}

// GetQuoteByShareToken loads a quote for the public viewer (no auth required).
func (db *DB) GetQuoteByShareToken(ctx context.Context, token string) (*models.QuoteWithDetails, error) {
	raw, _, err := db.client.From("quotes").
		Select("*,client:clients(*),line_items(*)", "exact", false).
		Eq("share_token", token).
		Single().
		Execute()
	if err != nil {
		return nil, fmt.Errorf("get quote by token: %w", err)
	}
	var q models.QuoteWithDetails
	return &q, decode(raw, &q)
}

// NextQuoteNumber generates the next sequential quote number for a user
// via a Postgres function to avoid race conditions.
func (db *DB) NextQuoteNumber(ctx context.Context, userID string) (string, error) {
	raw := db.client.Rpc("next_quote_number", "", map[string]interface{}{
		"p_user_id": userID,
	})
	if raw == "" {
		return "QF-001", nil
	}
	var result string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "QF-001", nil
	}
	if result == "" {
		return "QF-001", nil
	}
	return result, nil
}

// CountQuotesThisMonth returns how many quotes the user has created or duplicated in the current calendar month.
// Uses quote_events so that deleting a quote does not free up a slot (prevents create-delete-create abuse).
func (db *DB) CountQuotesThisMonth(ctx context.Context, userID string) (int, error) {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	raw, _, err := db.client.From("quote_events").
		Select("id", "exact", false).
		Eq("user_id", userID).
		In("event_type", []string{"created", "duplicated"}).
		Gte("occurred_at", monthStart.Format(time.RFC3339)).
		Execute()
	if err != nil {
		return 0, err
	}
	var rows []struct{ ID string `json:"id"` }
	if err := decode(raw, &rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (db *DB) CreateQuote(ctx context.Context, q *models.Quote, items []models.LineItemInput) error {
	now := time.Now()
	q.CreatedAt = now
	q.UpdatedAt = now
	q.ExpiresAt = now.AddDate(0, 0, q.ValidityDays)

	// Calculate totals from line items
	var subtotal float64
	for _, item := range items {
		subtotal += item.Quantity * item.UnitPrice
	}
	q.Subtotal = math.Round(subtotal*100) / 100
	if q.TaxExempt {
		q.TaxAmount = 0
	} else {
		q.TaxAmount = math.Round(subtotal*q.TaxRate/100*100) / 100
	}
	q.Total = q.Subtotal + q.TaxAmount

	// Use map to avoid sending empty id/share_token (PostgreSQL rejects "" for UUID)
	row := map[string]interface{}{
		"user_id":           q.UserID,
		"client_id":         q.ClientID,
		"quote_number":      q.QuoteNumber,
		"title":             q.Title,
		"status":            string(q.Status),
		"currency":          q.Currency,
		"subtotal":          q.Subtotal,
		"tax_rate":          q.TaxRate,
		"tax_exempt":        q.TaxExempt,
		"tax_amount":        q.TaxAmount,
		"total":             q.Total,
		"validity_days":     q.ValidityDays,
		"expires_at":        q.ExpiresAt,
		"notes":             q.Notes,
		"deposit":           q.Deposit,
		"payment_method":    q.PaymentMethod,
		"delivery_timeline":  q.DeliveryTimeline,
		"revisions":         q.Revisions,
		"require_signature": q.RequireSignature,
		"track_views":       q.TrackViews,
		"send_reminder":     q.SendReminder,
		"created_at":        now,
		"updated_at":        now,
	}
	if q.TeamID != nil && *q.TeamID != "" {
		row["team_id"] = *q.TeamID
	}
	raw, _, err := db.client.From("quotes").
		Insert(row, false, "", "representation", "").
		Execute()
	if err != nil {
		return fmt.Errorf("create quote: %w", err)
	}
	var results []models.Quote
	if err := decode(raw, &results); err != nil || len(results) == 0 {
		return fmt.Errorf("decoding created quote: %w", err)
	}
	*q = results[0]

	// Insert line items — use maps to avoid struct serialization issues
	liRows := make([]map[string]interface{}, 0, len(items))
	for i, item := range items {
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "Line item"
		}
		liRows = append(liRows, map[string]interface{}{
			"quote_id":     q.ID,
			"position":     i,
			"description":  desc,
			"quantity":     item.Quantity,
			"unit_price":   item.UnitPrice,
		})
	}
	if len(liRows) > 0 {
		_, _, err = db.client.From("line_items").
			Insert(liRows, false, "", "representation", "").
			Execute()
		if err != nil {
			return fmt.Errorf("create line items: %w", err)
		}
	}
	return nil
}

// UpdateQuote patches non-nil fields on a quote and optionally replaces all line items.
func (db *DB) UpdateQuote(ctx context.Context, id, userID string, req *models.UpdateQuoteRequest) (*models.QuoteWithDetails, error) {
	fields := map[string]interface{}{
		"updated_at": time.Now(),
	}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Currency != nil {
		fields["currency"] = *req.Currency
	}
	if req.ValidityDays != nil {
		fields["validity_days"] = *req.ValidityDays
	}
	if req.Notes != nil {
		fields["notes"] = *req.Notes
	}
	if req.Deposit != nil {
		fields["deposit"] = *req.Deposit
	}
	if req.PaymentMethod != nil {
		fields["payment_method"] = *req.PaymentMethod
	}
	if req.DeliveryTimeline != nil {
		fields["delivery_timeline"] = *req.DeliveryTimeline
	}
	if req.Revisions != nil {
		fields["revisions"] = *req.Revisions
	}
	if req.TaxExempt != nil {
		fields["tax_exempt"] = *req.TaxExempt
	}
	if req.TaxRate != nil {
		fields["tax_rate"] = *req.TaxRate
	}
	if req.RequireSignature != nil {
		fields["require_signature"] = *req.RequireSignature
	}
	if req.TrackViews != nil {
		fields["track_views"] = *req.TrackViews
	}
	if req.SendReminder != nil {
		fields["send_reminder"] = *req.SendReminder
	}

	// If line items are provided, recalculate totals
	if len(req.LineItems) > 0 {
		var subtotal float64
		for _, item := range req.LineItems {
			subtotal += item.Quantity * item.UnitPrice
		}
		fields["subtotal"] = math.Round(subtotal*100) / 100

		taxExempt := false
		if req.TaxExempt != nil {
			taxExempt = *req.TaxExempt
		}
		taxRate := 0.0
		if req.TaxRate != nil {
			taxRate = *req.TaxRate
		}

		if taxExempt {
			fields["tax_amount"] = 0.0
		} else {
			// If tax rate wasn't provided in the update, we need the existing value
			if req.TaxRate == nil {
				existing, err := db.GetQuote(ctx, id, userID)
				if err != nil {
					return nil, err
				}
				taxRate = existing.TaxRate
				if req.TaxExempt == nil {
					taxExempt = existing.TaxExempt
				}
			}
			if taxExempt {
				fields["tax_amount"] = 0.0
			} else {
				fields["tax_amount"] = math.Round(subtotal*taxRate/100*100) / 100
			}
		}
		taxAmt, _ := fields["tax_amount"].(float64)
		fields["total"] = fields["subtotal"].(float64) + taxAmt
	}

	// Recalculate expires_at if validity_days changed
	if req.ValidityDays != nil {
		fields["expires_at"] = time.Now().AddDate(0, 0, *req.ValidityDays)
	}

	teamID, uid := db.userOrTeamFilter(ctx, userID)
	q := db.client.From("quotes").Update(fields, "*", "").Eq("id", id)
	if teamID != "" {
		q = q.Eq("team_id", teamID)
	} else {
		q = q.Eq("user_id", uid)
	}
	_, _, err := q.Execute()
	if err != nil {
		return nil, fmt.Errorf("update quote: %w", err)
	}

	// If line items provided, delete old ones and insert new (use maps like CreateQuote)
	if len(req.LineItems) > 0 {
		_, _, _ = db.client.From("line_items").
			Delete("*", "").
			Eq("quote_id", id).
			Execute()

		liRows := make([]map[string]interface{}, 0, len(req.LineItems))
		for i, item := range req.LineItems {
			desc := strings.TrimSpace(item.Description)
			if desc == "" {
				desc = "Line item"
			}
			liRows = append(liRows, map[string]interface{}{
				"quote_id":    id,
				"position":    i,
				"description": desc,
				"quantity":    item.Quantity,
				"unit_price":  item.UnitPrice,
			})
		}
		_, _, err = db.client.From("line_items").
			Insert(liRows, false, "", "representation", "").
			Execute()
		if err != nil {
			return nil, fmt.Errorf("update line items: %w", err)
		}
	}

	return db.GetQuote(ctx, id, userID)
}

// MarkQuoteAsPaid sets paid_at on an accepted quote. Returns error if not accepted.
func (db *DB) MarkQuoteAsPaid(ctx context.Context, id, userID string) (*models.QuoteWithDetails, error) {
	quote, err := db.GetQuote(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if quote.Status != models.StatusAccepted {
		return nil, fmt.Errorf("quote must be accepted before marking as paid")
	}
	if quote.PaidAt != nil {
		return quote, nil // already paid
	}
	now := time.Now()
	teamID, uid := db.userOrTeamFilter(ctx, userID)
	q := db.client.From("quotes").Update(map[string]interface{}{"paid_at": now, "updated_at": now}, "*", "").Eq("id", id).Eq("status", "accepted")
	if teamID != "" {
		q = q.Eq("team_id", teamID)
	} else {
		q = q.Eq("user_id", uid)
	}
	_, _, err = q.Execute()
	if err != nil {
		return nil, fmt.Errorf("mark paid: %w", err)
	}
	return db.GetQuote(ctx, id, userID)
}

func (db *DB) UpdateQuoteStatus(ctx context.Context, id, userID string, status models.QuoteStatus) error {
	update := map[string]interface{}{
		"status":     string(status),
		"updated_at": time.Now(),
	}
	if status == models.StatusAccepted {
		now := time.Now()
		update["accepted_at"] = now
	}
	if status == models.StatusSent {
		now := time.Now()
		update["sent_at"] = now
	}
	teamID, uid := db.userOrTeamFilter(ctx, userID)
	q := db.client.From("quotes").Update(update, "*", "").Eq("id", id)
	if teamID != "" {
		q = q.Eq("team_id", teamID)
	} else {
		q = q.Eq("user_id", uid)
	}
	_, _, err := q.Execute()
	return err
}

func (db *DB) DuplicateQuote(ctx context.Context, id, userID string) (*models.Quote, error) {
	// Fetch original with line items
	original, err := db.GetQuote(ctx, id, userID)
	if err != nil {
		return nil, err
	}

	// New quote number
	newNum, err := db.NextQuoteNumber(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Build copy
	newQuote := models.Quote{
		UserID:           original.UserID,
		ClientID:         original.ClientID,
		TeamID:           original.TeamID,
		QuoteNumber:      newNum,
		Title:            original.Title + " (Copy)",
		Status:           models.StatusDraft,
		Currency:         original.Currency,
		ValidityDays:     original.ValidityDays,
		Notes:            original.Notes,
		Deposit:          original.Deposit,
		PaymentMethod:    original.PaymentMethod,
		DeliveryTimeline: original.DeliveryTimeline,
		Revisions:        original.Revisions,
		TaxExempt:        original.TaxExempt,
		TaxRate:          original.TaxRate,
		RequireSignature: original.RequireSignature,
		TrackViews:       original.TrackViews,
		SendReminder:     original.SendReminder,
	}

	// Build line item inputs from original
	liInputs := make([]models.LineItemInput, len(original.LineItems))
	for i, li := range original.LineItems {
		liInputs[i] = models.LineItemInput{
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
		}
	}

	return &newQuote, db.CreateQuote(ctx, &newQuote, liInputs)
}

// IncrementViewCount atomically bumps the view counter via a Supabase RPC function.
// The SQL function increment_view_count(quote_id uuid) is defined in 001_init.sql.
func (db *DB) IncrementViewCount(ctx context.Context, quoteID string) error {
	db.client.Rpc("increment_view_count", "", map[string]interface{}{
		"quote_id": quoteID,
	})
	return nil
}

func (db *DB) AcceptQuote(ctx context.Context, token string, sigName string) (*models.Quote, error) {
	now := time.Now()
	upd := map[string]interface{}{
		"status":      "accepted",
		"accepted_at": now,
		"updated_at":  now,
	}
	if sigName != "" {
		upd["accepted_by_name"] = strings.TrimSpace(sigName)
	}
	raw, _, err := db.client.From("quotes").
		Update(upd, "*", "").
		Eq("share_token", token).
		Eq("status", "sent").
		Execute()
	if err != nil {
		return nil, fmt.Errorf("accept quote: %w", err)
	}
	var results []models.Quote
	if err := decode(raw, &results); err != nil || len(results) == 0 {
		return nil, fmt.Errorf("quote not found, already accepted, or expired")
	}
	return &results[0], nil
}

func (db *DB) DeleteQuote(ctx context.Context, id, userID string) error {
	teamID, uid := db.userOrTeamFilter(ctx, userID)
	q := db.client.From("quotes").Delete("*", "").Eq("id", id)
	if teamID != "" {
		q = q.Eq("team_id", teamID)
	} else {
		q = q.Eq("user_id", uid)
	}
	_, _, err := q.Execute()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE NOTES
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) GetNotesByQuoteID(ctx context.Context, quoteID string) ([]models.QuoteNote, error) {
	raw, _, err := db.client.From("quote_notes").
		Select("*", "exact", false).
		Eq("quote_id", quoteID).
		Order("created_at", &postgrest.OrderOpts{Ascending: true}).
		Execute()
	if err != nil {
		return nil, err
	}
	var notes []models.QuoteNote
	return notes, decode(raw, &notes)
}

func (db *DB) GetNotesByShareToken(ctx context.Context, token string) ([]models.QuoteNote, error) {
	quote, err := db.GetQuoteByShareToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return db.GetNotesByQuoteID(ctx, quote.ID)
}

func (db *DB) AddNote(ctx context.Context, quoteID, authorType, authorName, message, noteType string) (*models.QuoteNote, error) {
	if noteType == "" {
		noteType = "message"
	}
	row := map[string]interface{}{
		"quote_id":    quoteID,
		"author_type": authorType,
		"author_name": authorName,
		"message":     message,
		"note_type":   noteType,
	}
	if authorType == "client" {
		row["read_at"] = nil
	} else {
		row["read_at"] = time.Now()
	}
	raw, _, err := db.client.From("quote_notes").
		Insert(row, false, "", "representation", "").
		Execute()
	if err != nil {
		return nil, err
	}
	var notes []models.QuoteNote
	if err := decode(raw, &notes); err != nil || len(notes) == 0 {
		return nil, fmt.Errorf("insert note failed")
	}
	return &notes[0], nil
}

func (db *DB) MarkNotesAsRead(ctx context.Context, quoteID string) error {
	now := time.Now()
	_, _, err := db.client.From("quote_notes").
		Update(map[string]interface{}{"read_at": now}, "*", "").
		Eq("quote_id", quoteID).
		Eq("author_type", "client").
		Is("read_at", "null").
		Execute()
	return err
}

// GetQuoteIDsWithUnreadNotes returns quote IDs (for the given user) that have unread client notes.
func (db *DB) GetQuoteIDsWithUnreadNotes(ctx context.Context, userID string) (map[string]bool, error) {
	raw, _, err := db.client.From("quote_notes").
		Select("quote_id", "exact", false).
		Eq("author_type", "client").
		Is("read_at", "null").
		Execute()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		QuoteID string `json:"quote_id"`
	}
	if err := decode(raw, &rows); err != nil {
		return nil, err
	}
	// Filter to only quotes belonging to this user
	quoteIDs := make(map[string]bool)
	for _, r := range rows {
		quoteIDs[r.QuoteID] = true
	}
	if len(quoteIDs) == 0 {
		return quoteIDs, nil
	}
	// Verify quotes belong to user
	ids := make([]string, 0, len(quoteIDs))
	for id := range quoteIDs {
		ids = append(ids, id)
	}
	raw2, _, err := db.client.From("quotes").
		Select("id", "exact", false).
		Eq("user_id", userID).
		In("id", ids).
		Execute()
	if err != nil {
		return nil, err
	}
	var quotes []struct{ ID string `json:"id"` }
	if err := decode(raw2, &quotes); err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	for _, q := range quotes {
		if quoteIDs[q.ID] {
			result[q.ID] = true
		}
	}
	return result, nil
}

// GetUnreadClientMessages returns unread client notes with quote and client details.
func (db *DB) GetUnreadClientMessages(ctx context.Context, userID string) ([]models.UnreadClientMessage, error) {
	quoteIDs, err := db.GetQuoteIDsWithUnreadNotes(ctx, userID)
	if err != nil || len(quoteIDs) == 0 {
		return nil, err
	}
	ids := make([]string, 0, len(quoteIDs))
	for id := range quoteIDs {
		ids = append(ids, id)
	}
	raw, _, err := db.client.From("quote_notes").
		Select("quote_id,message,author_name,note_type,created_at", "exact", false).
		Eq("author_type", "client").
		Is("read_at", "null").
		In("quote_id", ids).
		Order("created_at", &postgrest.OrderOpts{Ascending: false}).
		Execute()
	if err != nil {
		return nil, err
	}
	type noteRow struct {
		QuoteID    string    `json:"quote_id"`
		Message    string    `json:"message"`
		AuthorName string    `json:"author_name"`
		NoteType   string    `json:"note_type"`
		CreatedAt  time.Time `json:"created_at"`
	}
	var notes []noteRow
	if err := decode(raw, &notes); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var latest []noteRow
	for _, n := range notes {
		if !seen[n.QuoteID] {
			seen[n.QuoteID] = true
			latest = append(latest, n)
		}
	}
	if len(latest) == 0 {
		return nil, nil
	}
	raw2, _, err := db.client.From("quotes").
		Select("id,quote_number,client:clients(name)", "exact", false).
		In("id", ids).
		Execute()
	if err != nil {
		return nil, err
	}
	type quoteRow struct {
		ID          string `json:"id"`
		QuoteNumber string `json:"quote_number"`
		Client      struct {
			Name string `json:"name"`
		} `json:"client"`
	}
	var quotes []quoteRow
	if err := decode(raw2, &quotes); err != nil {
		return nil, err
	}
	qmap := make(map[string]quoteRow)
	for _, q := range quotes {
		qmap[q.ID] = q
	}
	result := make([]models.UnreadClientMessage, 0, len(latest))
	for _, n := range latest {
		q, ok := qmap[n.QuoteID]
		if !ok {
			continue
		}
		noteType := n.NoteType
		if noteType == "" {
			noteType = "message"
		}
		result = append(result, models.UnreadClientMessage{
			QuoteID:     n.QuoteID,
			QuoteNumber: q.QuoteNumber,
			ClientName:  q.Client.Name,
			AuthorName:  n.AuthorName,
			Message:     n.Message,
			NoteType:    noteType,
			CreatedAt:   n.CreatedAt,
		})
	}
	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE TEMPLATES
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) ListTemplates(ctx context.Context, userID string) ([]models.QuoteTemplate, error) {
	raw, _, err := db.client.From("quote_templates").
		Select("*", "exact", false).
		Eq("user_id", userID).
		Order("created_at", &postgrest.OrderOpts{Ascending: false}).
		Execute()
	if err != nil {
		return nil, err
	}
	var templates []models.QuoteTemplate
	if err := decode(raw, &templates); err != nil {
		return nil, err
	}
	for i := range templates {
		items, _ := db.getTemplateLineItems(ctx, templates[i].ID)
		templates[i].LineItems = items
	}
	return templates, nil
}

func (db *DB) getTemplateLineItems(ctx context.Context, templateID string) ([]models.TemplateLineItem, error) {
	raw, _, err := db.client.From("template_line_items").
		Select("*", "exact", false).
		Eq("template_id", templateID).
		Order("position", &postgrest.OrderOpts{Ascending: true}).
		Execute()
	if err != nil {
		return nil, err
	}
	var items []models.TemplateLineItem
	if err := decode(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (db *DB) CreateTemplate(ctx context.Context, userID string, req *models.CreateTemplateRequest) (*models.QuoteTemplate, error) {
	row := map[string]interface{}{
		"user_id":            userID,
		"name":               req.Name,
		"title":              defaultIfEmpty(req.Title, "Quote"),
		"currency":           req.Currency,
		"validity_days":      req.ValidityDays,
		"notes":              req.Notes,
		"deposit":            req.Deposit,
		"payment_method":     req.PaymentMethod,
		"delivery_timeline":   req.DeliveryTimeline,
		"revisions":          req.Revisions,
		"tax_exempt":         req.TaxExempt,
		"tax_rate":           req.TaxRate,
		"require_signature":  req.RequireSignature,
		"track_views":        req.TrackViews,
		"send_reminder":      req.SendReminder,
	}
	raw, _, err := db.client.From("quote_templates").
		Insert(row, false, "", "representation", "").
		Execute()
	if err != nil {
		return nil, err
	}
	var templates []models.QuoteTemplate
	if err := decode(raw, &templates); err != nil || len(templates) == 0 {
		return nil, fmt.Errorf("create template failed")
	}
	tpl := &templates[0]

	liRows := make([]map[string]interface{}, 0, len(req.LineItems))
	for i, item := range req.LineItems {
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "Line item"
		}
		liRows = append(liRows, map[string]interface{}{
			"template_id":  tpl.ID,
			"position":     i,
			"description":  desc,
			"quantity":     item.Quantity,
			"unit_price":   item.UnitPrice,
		})
	}
	if len(liRows) > 0 {
		_, _, err = db.client.From("template_line_items").
			Insert(liRows, false, "", "representation", "").
			Execute()
		if err != nil {
			return nil, fmt.Errorf("create template line items: %w", err)
		}
	}
	tpl.LineItems, _ = db.getTemplateLineItems(ctx, tpl.ID)
	return tpl, nil
}

func (db *DB) CreateTemplateFromQuote(ctx context.Context, userID string, name, quoteID string) (*models.QuoteTemplate, error) {
	quote, err := db.GetQuote(ctx, quoteID, userID)
	if err != nil {
		return nil, err
	}
	items := make([]models.LineItemInput, 0, len(quote.LineItems))
	for _, li := range quote.LineItems {
		items = append(items, models.LineItemInput{
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
		})
	}
	req := &models.CreateTemplateRequest{
		Name:              name,
		Title:             quote.Title,
		Currency:          quote.Currency,
		ValidityDays:      quote.ValidityDays,
		Notes:             quote.Notes,
		Deposit:           quote.Deposit,
		PaymentMethod:     quote.PaymentMethod,
		DeliveryTimeline:  quote.DeliveryTimeline,
		Revisions:         quote.Revisions,
		TaxExempt:         quote.TaxExempt,
		TaxRate:           quote.TaxRate,
		RequireSignature:  quote.RequireSignature,
		TrackViews:        quote.TrackViews,
		SendReminder:      quote.SendReminder,
		LineItems:         items,
	}
	return db.CreateTemplate(ctx, userID, req)
}

func (db *DB) DeleteTemplate(ctx context.Context, templateID, userID string) error {
	_, _, err := db.client.From("quote_templates").
		Delete("*", "").
		Eq("id", templateID).
		Eq("user_id", userID).
		Execute()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// DASHBOARD STATS
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) GetDashboardStats(ctx context.Context, userID string, currencyFilter string) (*models.DashboardStats, error) {
	// Fetch currencies used (distinct) for tab buttons
	curRaw, _, err := db.client.From("quotes").
		Select("currency", "exact", false).
		Eq("user_id", userID).
		Execute()
	if err != nil {
		return nil, err
	}
	var curRows []struct{ Currency string `json:"currency"` }
	if err := decode(curRaw, &curRows); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var currenciesUsed []string
	for _, r := range curRows {
		if r.Currency != "" && !seen[r.Currency] {
			seen[r.Currency] = true
			currenciesUsed = append(currenciesUsed, r.Currency)
		}
	}
	// Sort for consistent tab order: JMD, USD, TTD, BBD
	order := map[string]int{"JMD": 0, "USD": 1, "TTD": 2, "BBD": 3}
	sort.Slice(currenciesUsed, func(i, j int) bool {
		return order[currenciesUsed[i]] < order[currenciesUsed[j]]
	})

	// Build quotes query
	q := db.client.From("quotes").
		Select("status,total,currency,created_at,accepted_at", "exact", false).
		Eq("user_id", userID).
		Gte("created_at", time.Now().AddDate(0, -12, 0).Format(time.RFC3339))
	if currencyFilter != "" {
		q = q.Eq("currency", currencyFilter)
	}
	raw, _, err := q.Execute()
	if err != nil {
		return nil, err
	}

	type row struct {
		Status     string     `json:"status"`
		Total      float64    `json:"total"`
		Currency   string     `json:"currency"`
		CreatedAt  time.Time  `json:"created_at"`
		AcceptedAt *time.Time `json:"accepted_at"`
	}
	var rows []row
	if err := decode(raw, &rows); err != nil {
		return nil, err
	}

	now := time.Now()
	thisMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastMonthStart := thisMonthStart.AddDate(0, -1, 0)

	stats := &models.DashboardStats{CurrenciesUsed: currenciesUsed}
	for _, r := range rows {
		switch r.Status {
		case "draft":
			stats.DraftCount++
		case "sent":
			stats.SentCount++
		case "accepted":
			stats.AcceptedCount++
		case "expired":
			stats.ExpiredCount++
		}
		stats.TotalQuotesAllTime++

		if r.CreatedAt.After(thisMonthStart) {
			stats.TotalQuotedThisMonth += r.Total
			if r.Status == "accepted" {
				stats.QuotesAcceptedThisMonth++
			}
		} else if r.CreatedAt.After(lastMonthStart) {
			stats.TotalQuotedLastMonth += r.Total
			if r.Status == "accepted" {
				stats.QuotesAcceptedLastMonth++
			}
		}
	}

	// When "All" (no currency filter): zero out money fields — mixing currencies is meaningless
	if currencyFilter == "" {
		stats.TotalQuotedThisMonth = 0
		stats.TotalQuotedLastMonth = 0
		stats.QuotedChangePercent = 0
		stats.AvgQuoteValue = 0
	} else {
		// Change percent
		if stats.TotalQuotedLastMonth > 0 {
			stats.QuotedChangePercent = math.Round(
				(stats.TotalQuotedThisMonth-stats.TotalQuotedLastMonth)/stats.TotalQuotedLastMonth*100,
			)
		}
		// Avg quote value
		if stats.TotalQuotesAllTime > 0 {
			var totalAll float64
			for _, r := range rows {
				totalAll += r.Total
			}
			stats.AvgQuoteValue = math.Round(totalAll/float64(stats.TotalQuotesAllTime)*100) / 100
		}
	}

	// Quotes created this month: use quote_events so it matches free tier limit (deletion doesn't free a slot)
	stats.QuotesCreatedThisMonth, _ = db.CountQuotesThisMonth(ctx, userID)

	// Acceptance rate (sent + accepted out of all non-draft)
	nonDraft := stats.SentCount + stats.AcceptedCount + stats.ExpiredCount
	if nonDraft > 0 {
		stats.AcceptanceRate = math.Round(float64(stats.AcceptedCount)/float64(nonDraft)*100*10) / 10
	}

	// Recent activity
	stats.RecentActivity, _ = db.GetRecentActivity(ctx, userID, 10)

	return stats, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ACTIVITY / EVENTS
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) LogEvent(ctx context.Context, userID, quoteID, eventType, message string) error {
	row := map[string]interface{}{
		"user_id":     userID,
		"quote_id":    quoteID,
		"event_type":  eventType,
		"message":     message,
		"occurred_at": time.Now(),
	}
	_, _, err := db.client.From("quote_events").
		Insert(row, false, "", "representation", "").
		Execute()
	return err
}

func (db *DB) GetRecentActivity(ctx context.Context, userID string, limit int) ([]models.ActivityItem, error) {
	raw, _, err := db.client.From("quote_events").
		Select("*", "exact", false).
		Eq("user_id", userID).
		Order("occurred_at", &postgrest.OrderOpts{Ascending: false}).
		Limit(limit, "").
		Execute()
	if err != nil {
		return nil, err
	}
	type eventRow struct {
		ID         string    `json:"id"`
		QuoteID    string    `json:"quote_id"`
		EventType  string    `json:"event_type"`
		Message    string    `json:"message"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	var events []eventRow
	if err := decode(raw, &events); err != nil {
		return nil, err
	}
	items := make([]models.ActivityItem, len(events))
	for i, e := range events {
		items[i] = models.ActivityItem{
			ID:         e.ID,
			Type:       e.EventType,
			Message:    e.Message,
			QuoteID:    e.QuoteID,
			OccurredAt: e.OccurredAt,
		}
	}
	return items, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CRON: REMINDERS & EXPIRING NOTIFICATIONS
// ─────────────────────────────────────────────────────────────────────────────

// GetQuotesNeedingClientReminder returns sent quotes with send_reminder=true,
// expires_at in the next 3 days, and reminder_sent_at IS NULL.
func (db *DB) GetQuotesNeedingClientReminder(ctx context.Context) ([]models.QuoteWithDetails, error) {
	now := time.Now()
	windowEnd := now.AddDate(0, 0, 3)
	raw, _, err := db.client.From("quotes").
		Select("*,client:clients(*),line_items(*)", "exact", false).
		Eq("status", "sent").
		Eq("send_reminder", "true").
		Is("reminder_sent_at", "null").
		Gte("expires_at", now.Format(time.RFC3339)).
		Lte("expires_at", windowEnd.Format(time.RFC3339)).
		Execute()
	if err != nil {
		return nil, err
	}
	var quotes []models.QuoteWithDetails
	if err := decode(raw, &quotes); err != nil {
		return nil, err
	}
	return quotes, nil
}

// GetQuotesNeedingFreelancerExpiringNotification returns sent quotes expiring in 3 days
// whose owner has notify_expiring=true and no existing "expiring" event.
func (db *DB) GetQuotesNeedingFreelancerExpiringNotification(ctx context.Context) ([]models.QuoteWithDetails, error) {
	now := time.Now()
	windowEnd := now.AddDate(0, 0, 3)
	raw, _, err := db.client.From("quotes").
		Select("*,client:clients(*),line_items(*)", "exact", false).
		Eq("status", "sent").
		Gte("expires_at", now.Format(time.RFC3339)).
		Lte("expires_at", windowEnd.Format(time.RFC3339)).
		Execute()
	if err != nil {
		return nil, err
	}
	var quotes []models.QuoteWithDetails
	if err := decode(raw, &quotes); err != nil {
		return nil, err
	}
	var result []models.QuoteWithDetails
	for _, q := range quotes {
		profile, err := db.GetProfile(ctx, q.UserID)
		if err != nil || profile == nil || !profile.NotifyExpiring {
			continue
		}
		hasExpiring, _ := db.HasExpiringEvent(ctx, q.ID)
		if hasExpiring {
			continue
		}
		result = append(result, q)
	}
	return result, nil
}

// HasExpiringEvent returns true if quote_events has an "expiring" event for this quote.
func (db *DB) HasExpiringEvent(ctx context.Context, quoteID string) (bool, error) {
	raw, _, err := db.client.From("quote_events").
		Select("id", "exact", false).
		Eq("quote_id", quoteID).
		Eq("event_type", "expiring").
		Limit(1, "").
		Execute()
	if err != nil {
		return false, err
	}
	var rows []struct{ ID string `json:"id"` }
	if err := decode(raw, &rows); err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// MarkReminderSent sets reminder_sent_at on the quote.
func (db *DB) MarkReminderSent(ctx context.Context, quoteID string) error {
	now := time.Now()
	_, _, err := db.client.From("quotes").
		Update(map[string]interface{}{"reminder_sent_at": now, "updated_at": now}, "*", "").
		Eq("id", quoteID).
		Execute()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// TEAMS
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) GetTeamByUserID(ctx context.Context, userID string) (*models.Team, error) {
	profile, err := db.GetProfile(ctx, userID)
	if err != nil || profile == nil || profile.TeamID == nil || *profile.TeamID == "" {
		return nil, nil
	}
	return db.GetTeam(ctx, *profile.TeamID)
}

func (db *DB) GetTeam(ctx context.Context, teamID string) (*models.Team, error) {
	raw, _, err := db.client.From("teams").
		Select("*", "exact", false).
		Eq("id", teamID).
		Single().
		Execute()
	if err != nil {
		return nil, err
	}
	var t models.Team
	return &t, decode(raw, &t)
}

func (db *DB) ListTeamMembers(ctx context.Context, teamID string) ([]models.TeamMember, error) {
	raw, _, err := db.client.From("team_members").
		Select("*", "exact", false).
		Eq("team_id", teamID).
		Order("created_at", &postgrest.OrderOpts{Ascending: true}).
		Execute()
	if err != nil {
		return nil, err
	}
	var members []models.TeamMember
	return members, decode(raw, &members)
}

func (db *DB) CountTeamMembers(ctx context.Context, teamID string) (int, error) {
	raw, _, err := db.client.From("team_members").
		Select("id", "exact", false).
		Eq("team_id", teamID).
		Execute()
	if err != nil {
		return 0, err
	}
	var rows []struct{ ID string `json:"id"` }
	if err := decode(raw, &rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (db *DB) IsTeamMember(ctx context.Context, teamID, userID string) (bool, error) {
	raw, _, err := db.client.From("team_members").
		Select("id", "exact", false).
		Eq("team_id", teamID).
		Eq("user_id", userID).
		Limit(1, "").
		Execute()
	if err != nil {
		return false, err
	}
	var rows []struct{ ID string `json:"id"` }
	if err := decode(raw, &rows); err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (db *DB) AddTeamMember(ctx context.Context, teamID, userID, role string) error {
	row := map[string]interface{}{
		"team_id": teamID,
		"user_id": userID,
		"role":    role,
	}
	_, _, err := db.client.From("team_members").
		Insert(row, false, "", "representation", "").
		Execute()
	return err
}

func (db *DB) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	_, _, err := db.client.From("team_members").
		Delete("*", "").
		Eq("team_id", teamID).
		Eq("user_id", userID).
		Execute()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// API KEYS (Business plan)
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) ListAPIKeys(ctx context.Context, userID string) ([]models.APIKey, error) {
	raw, _, err := db.client.From("api_keys").
		Select("*", "exact", false).
		Eq("user_id", userID).
		Order("created_at", &postgrest.OrderOpts{Ascending: false}).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	var keys []models.APIKey
	if err := decode(raw, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func (db *DB) GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.APIKey, error) {
	raw, _, err := db.client.From("api_keys").
		Select("*", "exact", false).
		Eq("key_hash", keyHash).
		Single().
		Execute()
	if err != nil {
		return nil, err
	}
	var k models.APIKey
	if err := decode(raw, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

func (db *DB) CreateAPIKey(ctx context.Context, userID, name, keyHash string) (*models.APIKey, error) {
	now := time.Now()
	row := map[string]interface{}{
		"user_id":    userID,
		"name":       name,
		"key_hash":   keyHash,
		"created_at": now,
	}
	raw, _, err := db.client.From("api_keys").
		Insert(row, false, "", "representation", "").
		Execute()
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	var results []models.APIKey
	if err := decode(raw, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no row returned")
	}
	return &results[0], nil
}

func (db *DB) DeleteAPIKey(ctx context.Context, id, userID string) error {
	_, _, err := db.client.From("api_keys").
		Delete("*", "").
		Eq("id", id).
		Eq("user_id", userID).
		Execute()
	return err
}

func (db *DB) UpdateAPIKeyLastUsed(ctx context.Context, id string) error {
	now := time.Now()
	_, _, err := db.client.From("api_keys").
		Update(map[string]interface{}{"last_used_at": now}, "*", "").
		Eq("id", id).
		Execute()
	return err
}
