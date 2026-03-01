package models

import (
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// USER
// ─────────────────────────────────────────────────────────────────────────────

// User represents an authenticated QuoteFlow account.
// The ID maps directly to Supabase Auth's user UUID.
type User struct {
	ID        string    `json:"id" db:"id"`
	Email     string    `json:"email" db:"email"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// PROFILE / SETTINGS (per user, one row)
// ─────────────────────────────────────────────────────────────────────────────

type Profile struct {
	ID               string    `json:"id" db:"id"`
	UserID           string    `json:"user_id" db:"user_id"`
	BusinessName     string    `json:"business_name" db:"business_name"`
	Profession       string    `json:"profession" db:"profession"`
	Address          string    `json:"address" db:"address"`
	Phone            string    `json:"phone" db:"phone"`
	EmailOnQuote     string    `json:"email_on_quote" db:"email_on_quote"`
	LogoURL          *string   `json:"logo_url" db:"logo_url"`
	BrandColor       string    `json:"brand_color" db:"brand_color"`
	DefaultCurrency  string    `json:"default_currency" db:"default_currency"`
	DefaultValidity  int       `json:"default_validity_days" db:"default_validity_days"`
	DefaultDeposit   string    `json:"default_deposit" db:"default_deposit"`
	DefaultRevisions string    `json:"default_revisions" db:"default_revisions"`
	DefaultNotes     string    `json:"default_notes" db:"default_notes"`
	DefaultPayment   string    `json:"default_payment" db:"default_payment"`
	TaxType          string    `json:"tax_type" db:"tax_type"`
	TaxRate          float64   `json:"tax_rate" db:"tax_rate"`
	TaxNumber        string    `json:"tax_number" db:"tax_number"`
	TaxExemptDefault bool      `json:"tax_exempt_default" db:"tax_exempt_default"`
	ShowTaxBreakdown bool      `json:"show_tax_breakdown" db:"show_tax_breakdown"`
	// Billing
	Plan             string  `json:"plan" db:"plan"` // "free", "pro", or "business"
	StripeCustomerID string  `json:"stripe_customer_id,omitempty" db:"stripe_customer_id"`
	TeamID           *string `json:"team_id,omitempty" db:"team_id"`
	// Payment preferences
	DefaultPaymentTiming  string  `json:"default_payment_timing" db:"default_payment_timing"`   // full, deposit, link_only
	PreferredUSDProcessor *string `json:"preferred_usd_processor,omitempty" db:"preferred_usd_processor"` // stripe, paypal
	// Notification preferences
	NotifyAccepted  bool `json:"notify_accepted" db:"notify_accepted"`
	NotifyViewed    bool `json:"notify_viewed" db:"notify_viewed"`
	NotifyExpiring  bool `json:"notify_expiring" db:"notify_expiring"`
	NotifyWeekly    bool `json:"notify_weekly" db:"notify_weekly"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" db:"updated_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// TEAM
// ─────────────────────────────────────────────────────────────────────────────

type Team struct {
	ID        string    `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type TeamMember struct {
	ID        string    `json:"id" db:"id"`
	TeamID    string    `json:"team_id" db:"team_id"`
	UserID    string    `json:"user_id" db:"user_id"`
	Role      string    `json:"role" db:"role"` // owner, admin, member
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	Email     string    `json:"email,omitempty" db:"-"` // joined from auth, not stored
}

// ─────────────────────────────────────────────────────────────────────────────
// CLIENT
// ─────────────────────────────────────────────────────────────────────────────

type Client struct {
	ID        string    `json:"id" db:"id"`
	UserID    string    `json:"user_id" db:"user_id"`
	TeamID    *string   `json:"team_id,omitempty" db:"team_id"`
	Name      string    `json:"name" db:"name"`
	Company   string    `json:"company" db:"company"`
	Email     string    `json:"email" db:"email"`
	Phone     string    `json:"phone" db:"phone"`
	Address   string    `json:"address" db:"address"`
	Notes     string    `json:"notes" db:"notes"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`

	// Computed / joined fields (not stored)
	QuoteCount     int     `json:"quote_count,omitempty" db:"-"`
	TotalQuoted    float64 `json:"total_quoted,omitempty" db:"-"`
	AcceptanceRate float64 `json:"acceptance_rate,omitempty" db:"-"`
}

// CreateClientRequest is the payload for POST /clients
type CreateClientRequest struct {
	Name    string `json:"name" validate:"required,min=1"`
	Company string `json:"company"`
	Email   string `json:"email" validate:"required,email"`
	Phone   string `json:"phone"`
	Address string `json:"address"`
	Notes   string `json:"notes"`
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE
// ─────────────────────────────────────────────────────────────────────────────

// QuoteStatus is the lifecycle state of a quote.
type QuoteStatus string

const (
	StatusDraft    QuoteStatus = "draft"
	StatusSent     QuoteStatus = "sent"
	StatusAccepted QuoteStatus = "accepted"
	StatusExpired  QuoteStatus = "expired"
	StatusDeclined QuoteStatus = "declined"
)

type Quote struct {
	ID           string      `json:"id" db:"id"`
	UserID       string      `json:"user_id" db:"user_id"`
	ClientID     string      `json:"client_id" db:"client_id"`
	QuoteNumber  string      `json:"quote_number" db:"quote_number"`   // e.g. "QF-019"
	Title        string      `json:"title" db:"title"`
	Status       QuoteStatus `json:"status" db:"status"`
	Currency     string      `json:"currency" db:"currency"`           // JMD, USD, TTD, BBD
	Subtotal     float64     `json:"subtotal" db:"subtotal"`
	TaxRate      float64     `json:"tax_rate" db:"tax_rate"`
	TaxExempt    bool        `json:"tax_exempt" db:"tax_exempt"`
	TaxAmount    float64     `json:"tax_amount" db:"tax_amount"`
	Total        float64     `json:"total" db:"total"`
	ValidityDays int         `json:"validity_days" db:"validity_days"`
	ExpiresAt    time.Time   `json:"expires_at" db:"expires_at"`
	Notes        string      `json:"notes" db:"notes"`
	// Terms
	Deposit          string `json:"deposit" db:"deposit"`
	PaymentMethod    string `json:"payment_method" db:"payment_method"`
	DeliveryTimeline string `json:"delivery_timeline" db:"delivery_timeline"`
	Revisions        string `json:"revisions" db:"revisions"`
	// Options
	RequireSignature bool `json:"require_signature" db:"require_signature"`
	TrackViews       bool `json:"track_views" db:"track_views"`
	SendReminder     bool `json:"send_reminder" db:"send_reminder"`
	// Tracking
	ViewCount       int        `json:"view_count" db:"view_count"`
	LastViewedAt    *time.Time `json:"last_viewed_at" db:"last_viewed_at"`
	ReminderSentAt  *time.Time `json:"reminder_sent_at,omitempty" db:"reminder_sent_at"`
	AcceptedAt      *time.Time `json:"accepted_at" db:"accepted_at"`
	AcceptedByName  string     `json:"accepted_by_name,omitempty" db:"accepted_by_name"`
	PaidAt          *time.Time `json:"paid_at,omitempty" db:"paid_at"`
	DepositPaidAt   *time.Time `json:"deposit_paid_at,omitempty" db:"deposit_paid_at"`
	FullyPaidAt    *time.Time `json:"fully_paid_at,omitempty" db:"fully_paid_at"`
	SentAt          *time.Time `json:"sent_at" db:"sent_at"`
	// Share
	ShareToken string   `json:"share_token" db:"share_token"` // unique token for public link
	TeamID     *string  `json:"team_id,omitempty" db:"team_id"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`

	// Joined fields
	Client         *Client    `json:"client,omitempty" db:"-"`
	LineItems      []LineItem `json:"line_items,omitempty" db:"-"`
	HasUnreadNotes bool       `json:"has_unread_notes,omitempty" db:"-"`
}

// QuoteWithDetails is returned on GET /quotes/:id with client + line items attached.
type QuoteWithDetails struct {
	Quote
	Client    Client     `json:"client"`
	LineItems []LineItem `json:"line_items"`
}

// CreateQuoteRequest is the payload for POST /quotes
type CreateQuoteRequest struct {
	ClientID         string     `json:"client_id" validate:"required,uuid"`
	Title            string     `json:"title" validate:"required,min=1"`
	Currency         string     `json:"currency" validate:"required,oneof=JMD USD TTD BBD"`
	ValidityDays     int        `json:"validity_days" validate:"required,min=1,max=365"`
	Notes            string     `json:"notes"`
	Deposit          string     `json:"deposit"`
	PaymentMethod    string     `json:"payment_method"`
	DeliveryTimeline string     `json:"delivery_timeline"`
	Revisions        string     `json:"revisions"`
	TaxExempt        bool       `json:"tax_exempt"`
	TaxRate          float64    `json:"tax_rate"`
	RequireSignature bool       `json:"require_signature"`
	TrackViews       bool       `json:"track_views"`
	SendReminder     bool       `json:"send_reminder"`
	LineItems        []LineItemInput `json:"line_items" validate:"required,min=1,dive"`
}

// UpdateQuoteRequest is the payload for PATCH /quotes/:id
type UpdateQuoteRequest struct {
	Title            *string          `json:"title"`
	Currency         *string          `json:"currency"`
	ValidityDays     *int             `json:"validity_days"`
	Notes            *string          `json:"notes"`
	Deposit          *string          `json:"deposit"`
	PaymentMethod    *string          `json:"payment_method"`
	DeliveryTimeline *string          `json:"delivery_timeline"`
	Revisions        *string          `json:"revisions"`
	TaxExempt        *bool            `json:"tax_exempt"`
	TaxRate          *float64         `json:"tax_rate"`
	RequireSignature *bool            `json:"require_signature"`
	TrackViews       *bool            `json:"track_views"`
	SendReminder     *bool            `json:"send_reminder"`
	LineItems        []LineItemInput  `json:"line_items"`
}

// SendQuoteRequest is the payload for POST /quotes/:id/send
type SendQuoteRequest struct {
	Channel string `json:"channel" validate:"required,oneof=email whatsapp link"`
	// Email-specific (required when channel=email)
	RecipientEmail string `json:"recipient_email"`
	// WhatsApp-specific (required when channel=whatsapp)
	RecipientPhone string `json:"recipient_phone"`
	// Optional custom message
	Message string `json:"message"`
}

// AcceptQuoteRequest is the payload for POST /q/:token/accept (public endpoint)
type AcceptQuoteRequest struct {
	SignatureName string `json:"signature_name"` // printed name for e-signature
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE NOTES
// ─────────────────────────────────────────────────────────────────────────────

type QuoteNote struct {
	ID         string     `json:"id" db:"id"`
	QuoteID    string     `json:"quote_id" db:"quote_id"`
	AuthorType string     `json:"author_type" db:"author_type"`   // client | freelancer
	AuthorName string     `json:"author_name" db:"author_name"`
	Message    string     `json:"message" db:"message"`
	NoteType   string     `json:"note_type" db:"note_type"`       // message | change_request
	ReadAt     *time.Time `json:"read_at,omitempty" db:"read_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}

// PostNoteRequest (client) — POST /q/:token/notes
type PostNoteRequest struct {
	Name     string `json:"name" validate:"required,min=1"`
	Message  string `json:"message" validate:"required,min=1"`
	NoteType string `json:"note_type"` // message (default) | change_request
}

// ReplyNoteRequest (freelancer) — POST /quotes/:id/notes
type ReplyNoteRequest struct {
	Message string `json:"message" validate:"required,min=1"`
}

// ─────────────────────────────────────────────────────────────────────────────
// LINE ITEM
// ─────────────────────────────────────────────────────────────────────────────

type LineItem struct {
	ID          string  `json:"id" db:"id"`
	QuoteID     string  `json:"quote_id" db:"quote_id"`
	Position    int     `json:"position" db:"position"`
	Description string  `json:"description" db:"description"`
	Quantity    float64 `json:"quantity" db:"quantity"`
	UnitPrice   float64 `json:"unit_price" db:"unit_price"`
	Total       float64 `json:"total" db:"total"` // quantity * unit_price
}

// LineItemInput is used in create/update requests
type LineItemInput struct {
	Description string  `json:"description"` // empty ok; backend uses "Line item" fallback
	Quantity    float64 `json:"quantity" validate:"required,min=0"`
	UnitPrice   float64 `json:"unit_price" validate:"required,min=0"`
}

// ─────────────────────────────────────────────────────────────────────────────
// QUOTE TEMPLATES
// ─────────────────────────────────────────────────────────────────────────────

type QuoteTemplate struct {
	ID                string             `json:"id" db:"id"`
	UserID            string             `json:"user_id" db:"user_id"`
	Name              string             `json:"name" db:"name"`
	Title             string             `json:"title" db:"title"`
	Currency          string             `json:"currency" db:"currency"`
	ValidityDays      int                `json:"validity_days" db:"validity_days"`
	Notes             string             `json:"notes" db:"notes"`
	Deposit           string             `json:"deposit" db:"deposit"`
	PaymentMethod     string             `json:"payment_method" db:"payment_method"`
	DeliveryTimeline  string             `json:"delivery_timeline" db:"delivery_timeline"`
	Revisions         string             `json:"revisions" db:"revisions"`
	TaxExempt         bool               `json:"tax_exempt" db:"tax_exempt"`
	TaxRate           float64            `json:"tax_rate" db:"tax_rate"`
	RequireSignature  bool               `json:"require_signature" db:"require_signature"`
	TrackViews        bool               `json:"track_views" db:"track_views"`
	SendReminder      bool               `json:"send_reminder" db:"send_reminder"`
	CreatedAt         time.Time          `json:"created_at" db:"created_at"`
	LineItems         []TemplateLineItem `json:"line_items,omitempty" db:"-"`
}

type TemplateLineItem struct {
	ID          string  `json:"id" db:"id"`
	TemplateID  string  `json:"template_id" db:"template_id"`
	Position    int     `json:"position" db:"position"`
	Description string  `json:"description" db:"description"`
	Quantity    float64 `json:"quantity" db:"quantity"`
	UnitPrice   float64 `json:"unit_price" db:"unit_price"`
}

// CreateTemplateRequest — POST /templates (from scratch)
type CreateTemplateRequest struct {
	Name              string           `json:"name" validate:"required,min=1"`
	Title             string           `json:"title"`
	Currency          string           `json:"currency" validate:"required,oneof=JMD USD TTD BBD"`
	ValidityDays      int              `json:"validity_days" validate:"required,min=1,max=365"`
	Notes             string           `json:"notes"`
	Deposit           string           `json:"deposit"`
	PaymentMethod     string           `json:"payment_method"`
	DeliveryTimeline  string           `json:"delivery_timeline"`
	Revisions         string           `json:"revisions"`
	TaxExempt         bool             `json:"tax_exempt"`
	TaxRate           float64          `json:"tax_rate"`
	RequireSignature  bool             `json:"require_signature"`
	TrackViews        bool             `json:"track_views"`
	SendReminder      bool             `json:"send_reminder"`
	LineItems         []LineItemInput   `json:"line_items" validate:"required,min=1,dive"`
}

// CreateTemplateFromQuoteRequest — POST /templates/from-quote (from existing quote)
type CreateTemplateFromQuoteRequest struct {
	Name    string `json:"name" validate:"required,min=1"`
	QuoteID string `json:"quote_id" validate:"required,uuid"`
}

// ─────────────────────────────────────────────────────────────────────────────
// DASHBOARD / ANALYTICS
// ─────────────────────────────────────────────────────────────────────────────

type DashboardStats struct {
	TotalQuotedThisMonth float64 `json:"total_quoted_this_month"`
	TotalQuotedLastMonth float64 `json:"total_quoted_last_month"`
	QuotedChangePercent  float64 `json:"quoted_change_percent"`

	QuotesAcceptedThisMonth int     `json:"quotes_accepted_this_month"`
	QuotesAcceptedLastMonth int     `json:"quotes_accepted_last_month"`

	AcceptanceRate     float64 `json:"acceptance_rate"`
	AvgQuoteValue      float64 `json:"avg_quote_value"`

	TotalQuotesAllTime     int `json:"total_quotes_all_time"`
	QuotesCreatedThisMonth int `json:"quotes_created_this_month"`
	DraftCount             int `json:"draft_count"`
	SentCount             int `json:"sent_count"`
	AcceptedCount         int `json:"accepted_count"`
	ExpiredCount          int `json:"expired_count"`

	// Currencies the user has used (for tab buttons). Empty = no tabs.
	CurrenciesUsed []string `json:"currencies_used"`

	// Recent activity feed items
	RecentActivity []ActivityItem `json:"recent_activity"`
}

type ActivityItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // accepted | viewed | expiring | created | sent
	Message     string    `json:"message"`
	QuoteID     string    `json:"quote_id,omitempty"`
	QuoteNumber string    `json:"quote_number,omitempty"`
	ClientName  string    `json:"client_name,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// UnreadClientMessage is a client note the freelancer hasn't seen yet.
type UnreadClientMessage struct {
	QuoteID       string    `json:"quote_id"`
	QuoteNumber   string    `json:"quote_number"`
	ClientName    string    `json:"client_name"`
	AuthorName    string    `json:"author_name"`    // name client used when posting
	Message       string    `json:"message"`
	NoteType      string    `json:"note_type"`      // message | change_request
	CreatedAt     time.Time `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// API KEYS (Business plan)
// ─────────────────────────────────────────────────────────────────────────────

type APIKey struct {
	ID         string     `json:"id" db:"id"`
	UserID     string     `json:"user_id" db:"user_id"`
	Name       string     `json:"name" db:"name"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty" db:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}

// CreateAPIKeyRequest — POST /api-keys
type CreateAPIKeyRequest struct {
	Name string `json:"name" validate:"required,min=1,max=100"`
}

// CreateAPIKeyResponse — raw key returned once on create
type CreateAPIKeyResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"` // qf_live_xxx — only shown once
	CreatedAt time.Time `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// PAYMENTS
// ─────────────────────────────────────────────────────────────────────────────

type PaymentProcessor string

const (
	ProcessorWiPay  PaymentProcessor = "wipay"
	ProcessorStripe PaymentProcessor = "stripe"
	ProcessorPayPal PaymentProcessor = "paypal"
)

type PaymentType string

const (
	PaymentTypeFull    PaymentType = "full"
	PaymentTypeDeposit PaymentType = "deposit"
	PaymentTypeBalance PaymentType = "balance"
)

type PaymentStatus string

const (
	PaymentStatusPending  PaymentStatus = "pending"
	PaymentStatusPaid     PaymentStatus = "paid"
	PaymentStatusFailed   PaymentStatus = "failed"
	PaymentStatusRefunded PaymentStatus = "refunded"
)

type PaymentAccount struct {
	ID               string           `json:"id" db:"id"`
	UserID           string           `json:"user_id" db:"user_id"`
	Processor        PaymentProcessor `json:"processor" db:"processor"`
	WiPayAccountID   string           `json:"wipay_account_id,omitempty" db:"wipay_account_id"`
	StripeAccountID  string           `json:"stripe_account_id,omitempty" db:"stripe_account_id"`
	PayPalMerchantID string           `json:"paypal_merchant_id,omitempty" db:"paypal_merchant_id"`
	IsActive         bool             `json:"is_active" db:"is_active"`
	CreatedAt        time.Time        `json:"created_at" db:"created_at"`
}

// PaymentAccountFull includes credentials (for internal payment service use only).
type PaymentAccountFull struct {
	PaymentAccount
	WiPayAPIKey       string `json:"wipay_api_key" db:"wipay_api_key"`
	StripeAccessToken string `json:"stripe_access_token" db:"stripe_access_token"`
	PayPalAccessToken string `json:"paypal_access_token" db:"paypal_access_token"`
}

type Payment struct {
	ID                 string           `json:"id" db:"id"`
	QuoteID            string           `json:"quote_id" db:"quote_id"`
	UserID             string           `json:"user_id" db:"user_id"`
	Processor          PaymentProcessor `json:"processor" db:"processor"`
	PaymentType        PaymentType      `json:"payment_type" db:"payment_type"`
	Amount             float64          `json:"amount" db:"amount"`
	PlatformFee        float64          `json:"platform_fee" db:"platform_fee"`
	NetAmount          float64          `json:"net_amount" db:"net_amount"`
	Currency           string           `json:"currency" db:"currency"`
	Status             PaymentStatus    `json:"status" db:"status"`
	ProcessorPaymentID string           `json:"processor_payment_id,omitempty" db:"processor_payment_id"`
	PaymentURL         string           `json:"payment_url,omitempty" db:"payment_url"`
	PaidAt             *time.Time       `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
}

type CreatePaymentLinkRequest struct {
	QuoteID     string      `json:"quote_id" validate:"required,uuid"`
	PaymentType PaymentType `json:"payment_type" validate:"required,oneof=full deposit balance"`
}

type PaymentLinkResponse struct {
	PaymentURL  string           `json:"payment_url"`
	Amount      float64          `json:"amount"`
	PlatformFee float64          `json:"platform_fee"`
	NetAmount   float64          `json:"net_amount"`
	Currency    string           `json:"currency"`
	PaymentType PaymentType      `json:"payment_type"`
	Processor   PaymentProcessor `json:"processor"`
}

type ConnectWiPayRequest struct {
	AccountID string `json:"account_id" validate:"required"`
	APIKey    string `json:"api_key" validate:"required"`
}

type ConnectPayPalRequest struct {
	MerchantID  string `json:"merchant_id" validate:"required"`
	AccessToken string `json:"access_token" validate:"required"`
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH
// ─────────────────────────────────────────────────────────────────────────────

type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
	Name     string `json:"name" validate:"required,min=1"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         User   `json:"user"`
}

// ─────────────────────────────────────────────────────────────────────────────
// API RESPONSE WRAPPERS
// ─────────────────────────────────────────────────────────────────────────────

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Message string      `json:"message,omitempty"`
}

type PaginatedResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Total   int         `json:"total"`
	Page    int         `json:"page"`
	Limit   int         `json:"limit"`
}
