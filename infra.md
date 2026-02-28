# QuoteFlow Backend — Architecture & Cursor Handoff Document

> **Purpose:** This document is a complete map of the Go backend as it was designed and built. Use it to understand every decision made, every known gap, and every feature that still needs to be implemented before this is production-ready.

---

## 1. What QuoteFlow Is

A SaaS quote-builder for Caribbean and Latin American freelancers. A freelancer creates a quote in the dashboard, sends it via **email or WhatsApp**, and the client opens a public link (no login required) to review and accept it. The freelancer gets notified instantly.

**Target markets:** Jamaica (JMD + GCT tax), Trinidad & Tobago (TTD + VAT), Barbados (BBD + VAT).  
**Pricing:** Free tier (3 quotes/month), Pro ($15/month, unlimited).

---

## 2. Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.22 |
| HTTP router | Chi v5 (`github.com/go-chi/chi/v5`) |
| Auth | Supabase Auth (JWT issued by Supabase, verified by Go) |
| Database | Supabase (PostgreSQL) via `supabase-go` client |
| JWT verification | `github.com/go-chi/jwtauth/v5` |
| Email | Resend API (raw HTTP, no SDK) |
| WhatsApp | Twilio API (raw HTTP, no SDK) |
| Config | `.env` file via `github.com/joho/godotenv` |

**The frontend is a separate React/TypeScript/Vite project** — it talks directly to Supabase for authentication (sign-up, login, session management) and talks to this Go backend for all application data (quotes, clients, profiles, dashboard).

---

## 3. Repository Structure

```
quoteflow-backend/
├── cmd/server/main.go              ← Entry point. Wires all deps, defines router, starts HTTP server
├── config/config.go                ← Loads all env vars. Panics if required vars are missing
├── internal/
│   ├── handlers/handlers.go        ← All HTTP handlers (one file, ~490 lines)
│   ├── middleware/auth.go          ← JWT verification middleware + context helpers
│   ├── models/models.go            ← All domain structs, request/response types (~295 lines)
│   ├── repository/db.go            ← All database operations via supabase-go (~520 lines)
│   └── services/
│       ├── notifications.go        ← Resend email + Twilio WhatsApp sending (~280 lines)
│       └── export.go               ← CSV export builder (~80 lines)
├── db/migrations/001_init.sql      ← Complete PostgreSQL schema (run once in Supabase SQL Editor)
├── .env.example                    ← All required env vars documented
├── Makefile                        ← Dev commands (run, build, test, docker)
├── Dockerfile                      ← Multi-stage build → ~8MB scratch image
└── .air.toml                       ← Hot-reload config for development
```

---

## 4. Authentication Architecture

### How it works

1. The **frontend** calls Supabase Auth directly (`supabase.auth.signInWithPassword()`). Supabase returns a **JWT access token**.
2. The frontend sends this token on every API request as `Authorization: Bearer <token>`.
3. The **Go backend** verifies the JWT signature using the Supabase JWT secret (`JWT_SECRET` env var — find it in Supabase project settings → API → JWT Secret).
4. The middleware extracts `sub` (user UUID) and `email` from the JWT claims and injects a `*models.User` into the request context.
5. Every handler calls `currentUser(r)` to get the authenticated user — no database lookup needed for auth.

### Middleware chain

```
All requests:
  RequestID → RealIP → Logger → Recoverer → Timeout(30s) → CORS

Protected routes additionally:
  RequireAuth(ja) ← verifies JWT, injects user into context
```

### Key detail for Cursor

The `JWT_SECRET` you set in `.env` **must match exactly** the JWT secret in your Supabase project settings. If these don't match, every authenticated request will return 401. Go to: Supabase Dashboard → Project Settings → API → JWT Secret.

---

## 5. Database Architecture

All database access goes through **one repository struct** (`repository.DB`) using the **Supabase service role key** (which bypasses Row Level Security on the server side). RLS is still enabled on all tables to protect direct Supabase API calls from the frontend.

### Tables

| Table | Purpose |
|---|---|
| `profiles` | One row per user. Business info, branding, defaults, notification prefs. Auto-created by trigger on signup. |
| `clients` | Freelancer's client list. Name, email, phone, address, notes. |
| `quotes` | Core entity. Status lifecycle: `draft → sent → accepted/expired/declined`. |
| `line_items` | Line items belonging to a quote. `total` column is a generated/computed column in Postgres (`quantity * unit_price`). |
| `quote_events` | Activity feed. Every significant action (created, sent, viewed, accepted, duplicated) writes a row here. |

### Views

| View | Purpose |
|---|---|
| `client_summary` | Extends `clients` with computed stats: `quote_count`, `total_quoted`, `acceptance_rate`. The client list and client detail endpoints query this view, not the base table. |

### Postgres Functions (called via Supabase RPC)

| Function | Purpose |
|---|---|
| `increment_view_count(quote_id uuid)` | Atomically bumps `view_count` and sets `last_viewed_at`. Called as a goroutine fire-and-forget when a client opens their quote link. |
| `expire_old_quotes()` | Sets `status = 'expired'` on all sent quotes past their `expires_at`. Meant to be called by a pg_cron job every hour: `SELECT cron.schedule('expire-quotes','0 * * * *','SELECT expire_old_quotes()')` |
| `handle_new_user()` | Trigger function. Fires on `INSERT` into `auth.users`. Auto-creates a `profiles` row so every user always has a profile. |
| `update_updated_at()` | Trigger function on profiles, clients, quotes. Auto-sets `updated_at = NOW()` on every update. |

### Key detail: `share_token`

Every quote is created with a unique `share_token` (base64url-encoded 24 random bytes, generated by Postgres via `encode(gen_random_bytes(24), 'base64url')`). The public URL is: `{QUOTE_LINK_BASE_URL}/{share_token}`. This is how clients access their quote without logging in.

---

## 6. API Endpoints — Complete Reference

### Base URL

```
Development: http://localhost:8080
Production:  Your Railway/Fly.io URL
```

### Public (no auth)

| Method | Path | Handler | What it does |
|---|---|---|---|
| `GET` | `/health` | `HealthCheck` | Returns `{status:"ok"}`. Used by uptime monitors. |
| `GET` | `/q/:token` | `PublicGetQuote` | Returns full quote + client + line items by share token. Increments view count (goroutine). Sends "viewed" email notification to freelancer if `track_views=true`. |
| `POST` | `/q/:token/accept` | `PublicAcceptQuote` | Sets quote status to `accepted`. Notifies freelancer by email (goroutine). Logs event. Body: `{signature_name?: string}`. |

### Authenticated (Bearer token required)

| Method | Path | Handler | What it does |
|---|---|---|---|
| `GET` | `/auth/me` | `GetMe` | Returns current user + profile. Used on app load. |
| `GET` | `/dashboard` | `GetDashboard` | Returns `DashboardStats` — monthly totals, acceptance rate, counts by status, recent activity feed. |
| `GET` | `/profile` | `GetProfile` | Returns full profile row. |
| `PUT` | `/profile` | `UpdateProfile` | Full upsert of profile (uses Supabase upsert on `user_id`). |
| `GET` | `/clients` | `ListClients` | Returns clients from `client_summary` view (includes stats). Ordered by `created_at DESC`. |
| `POST` | `/clients` | `CreateClient` | Creates client + logs event. Validates name+email required. |
| `GET` | `/clients/:id` | `GetClient` | Single client with stats (from `client_summary` view). |
| `PUT` | `/clients/:id` | `UpdateClient` | Full client update. Scoped to `user_id`. |
| `DELETE` | `/clients/:id` | `DeleteClient` | Deletes client. **Note: will fail if client has quotes** (FK constraint `ON DELETE RESTRICT` on `quotes.client_id`). |
| `GET` | `/quotes` | `ListQuotes` | All quotes with embedded `client`. Optional `?status=draft\|sent\|accepted\|expired`. |
| `POST` | `/quotes` | `CreateQuote` | Creates quote + line items in one call. Calculates totals. Generates sequential quote number (`QF-001`, `QF-002`…). Status starts as `draft`. |
| `GET` | `/quotes/export` | `ExportQuotesCSV` | Streams a `.csv` file with all quotes. Sets `Content-Disposition: attachment`. |
| `GET` | `/quotes/:id` | `GetQuote` | Single quote with `client` + `line_items` embedded. |
| `DELETE` | `/quotes/:id` | `DeleteQuote` | Hard delete. Scoped to `user_id`. |
| `POST` | `/quotes/:id/send` | `SendQuote` | Sends quote via `email`, `whatsapp`, or `link`. Updates status to `sent`. Logs event. Body: `{channel, recipient_email?, recipient_phone?, message?}`. |
| `POST` | `/quotes/:id/duplicate` | `DuplicateQuote` | Copies quote + all line items. New quote number. Status = `draft`. Title appended with `(Copy)`. Logs event. |

---

## 7. Data Flow for Key Operations

### Creating a Quote

```
POST /quotes
  → Validate: client_id, title, line_items required
  → NextQuoteNumber() → queries last quote_number, increments
  → CreateQuote() → INSERT into quotes (calculates subtotal/tax/total in Go)
                  → INSERT into line_items (batch)
  → LogEvent() → INSERT into quote_events
  → Return created quote (201)
```

**Note:** The `line_items.total` column is a Postgres generated column. Go does NOT need to pass a `total` value — Postgres computes it. However, Go still calculates `subtotal`, `tax_amount`, and `total` for the `quotes` row itself.

### Sending a Quote

```
POST /quotes/:id/send  {channel: "email"}
  → GetQuote() to verify ownership and get client details
  → GetProfile() to get sender's business name
  → switch channel:
      email:    SendQuoteByEmail() → Resend API
      whatsapp: SendQuoteViaWhatsApp() → Twilio API
      link:     no external call — just return the link
  → UpdateQuoteStatus() → set status="sent", sent_at=NOW()
  → LogEvent()
  → Return {message, quote_link, channel}
```

### Client Opening the Quote

```
GET /q/:token  (no auth)
  → GetQuoteByShareToken() → full quote + client + line_items
  → if track_views: go IncrementViewCount() + SendQuoteViewedNotification()
  → Return quote data

POST /q/:token/accept  (no auth)
  → AcceptQuote() → UPDATE quotes SET status='accepted', accepted_at=NOW() WHERE share_token=token
  → go: GetQuote() + GetProfile() → SendQuoteAcceptedNotification() + LogEvent()
  → Return {accepted: true, quote_number, message}
```

---

## 8. Notification Architecture

The `NotificationService` in `internal/services/notifications.go` handles two channels:

### Email — Resend

- Uses the **Resend HTTP API** directly (no SDK). Requires `RESEND_API_KEY`.
- Free tier: 3,000 emails/month — enough for early stage.
- Three email types implemented:
  1. `SendQuoteByEmail` — sends the quote link to the client with a styled HTML template
  2. `SendQuoteAcceptedNotification` — notifies freelancer when client accepts
  3. `SendQuoteViewedNotification` — notifies freelancer when client opens the link

### WhatsApp — Twilio

- Uses **Twilio WhatsApp Business API** via raw HTTP.
- Requires: `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN`, `TWILIO_WHATSAPP_NUMBER`.
- For testing, Twilio provides a sandbox: `whatsapp:+14155238886` (the client must first text "join [word]" to the sandbox number).
- For production, you need a verified Twilio WhatsApp Business sender.

### Goroutines

View tracking and accepted notifications run in **goroutines** (fire-and-forget). This means the HTTP response returns immediately and the notification sends async. If the goroutine fails, it fails silently — no retry logic is implemented.

---

## 9. Environment Variables — Complete List

```env
# Server
PORT=8080
ENV=development

# Supabase — get from: Project Settings → API
SUPABASE_URL=https://xxxx.supabase.co
SUPABASE_ANON_KEY=eyJ...
SUPABASE_SERVICE_ROLE_KEY=eyJ...    ← Used by Go backend only. Never expose to frontend.
SUPABASE_DB_URL=postgresql://postgres:[password]@db.xxxx.supabase.co:5432/postgres

# JWT — MUST match Supabase Project Settings → API → JWT Secret
JWT_SECRET=your-supabase-jwt-secret-here

# Email (Resend) — https://resend.com
RESEND_API_KEY=re_xxxx
EMAIL_FROM=quotes@yourdomain.com
EMAIL_FROM_NAME=QuoteFlow

# WhatsApp (Twilio) — https://twilio.com
TWILIO_ACCOUNT_SID=ACxxxx
TWILIO_AUTH_TOKEN=xxxx
TWILIO_WHATSAPP_NUMBER=whatsapp:+14155238886   ← sandbox number

# URLs
APP_URL=http://localhost:8080
FRONTEND_URL=http://localhost:5173
QUOTE_LINK_BASE_URL=http://localhost:5173/q    ← NOTE: points to FRONTEND, not backend

# CORS — comma-separated
ALLOWED_ORIGINS=http://localhost:5173
```

**Important:** `QUOTE_LINK_BASE_URL` should point to the **frontend** `/q/:token` route, not the backend. The frontend's `PublicQuotePage` then calls the backend API to load the quote data.

---

## 10. Known Bugs That Need Fixing

### BUG 1 — `go.mod` module path

**File:** `go.mod`  
**Problem:** The module is named `github.com/yourusername/quoteflow`. Every import in every file uses this placeholder. Before running, you must replace it throughout:

```bash
# Replace with your actual module path
find . -type f -name "*.go" | xargs sed -i 's|github.com/yourusername/quoteflow|github.com/yourname/quoteflow|g'
# Then update go.mod first line
```

**Fix:** Replace `yourusername` with your actual GitHub username or use a local path like `quoteflow` if not publishing.

---

### BUG 2 — `SendQuote` handler uses wrong variable

**File:** `internal/handlers/handlers.go` — `SendQuote` function  
**Problem:** The `recipient` variable is declared but never used:

```go
recipient := req.RecipientEmail
if recipient == "" {
    recipient = quote.Client.Email
}
// ← recipient is never passed to SendQuoteByEmail()
if err := h.notif.SendQuoteByEmail(quote, senderName); err != nil {
```

`SendQuoteByEmail` always sends to `quote.Client.Email` internally. If the caller wants to override the recipient (e.g. CC a different address), the override is silently discarded.  
**Fix:** Either pass `recipient` into `SendQuoteByEmail` as a parameter, or remove the override logic entirely and always use `quote.Client.Email`.

---

### BUG 3 — WhatsApp body is not URL-encoded

**File:** `internal/services/notifications.go` — `twilioSendWhatsApp`  
**Problem:** The Twilio form body is built with `fmt.Sprintf` but special characters (spaces, `&`, `+`, newlines in the message) are not URL-encoded. Twilio will receive a malformed request body.

```go
payload := strings.NewReader(fmt.Sprintf(
    "From=%s&To=%s&Body=%s",    // ← Body not encoded
    n.cfg.TwilioWhatsAppNumber, to, message,
))
```

**Fix:**

```go
data := url.Values{}
data.Set("From", n.cfg.TwilioWhatsAppNumber)
data.Set("To", to)
data.Set("Body", message)
payload := strings.NewReader(data.Encode())
req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
```

---

### BUG 4 — `AcceptQuote` does NOT validate quote status before accepting

**File:** `internal/repository/db.go` — `AcceptQuote`  
**Problem:** A client can accept a quote that is already `accepted`, `expired`, or `declined`. There is no status check:

```go
raw, _, err := db.client.From("quotes").
    Update(map[string]interface{}{"status": "accepted", ...}).
    Eq("share_token", token).   // ← no status check
    Execute()
```

**Fix:** Add `.Eq("status", "sent")` (or `.In("status", []string{"sent", "draft"})` if drafts can be accepted) to the update filter. If the update affects 0 rows, the quote was already accepted/expired — return an error.

---

### BUG 5 — Quote number race condition

**File:** `internal/repository/db.go` — `NextQuoteNumber`  
**Problem:** Two simultaneous quote creations by the same user could generate the same quote number. The current approach queries the last number then increments in Go — not atomic.  
**Fix:** Replace with a Postgres sequence or use a `SELECT ... FOR UPDATE` lock. The cleanest solution is a Postgres function:

```sql
CREATE OR REPLACE FUNCTION next_quote_number(p_user_id uuid)
RETURNS text AS $$
DECLARE
  next_seq int;
BEGIN
  SELECT COALESCE(MAX(CAST(SUBSTRING(quote_number FROM 4) AS int)), 0) + 1
  INTO next_seq
  FROM quotes WHERE user_id = p_user_id;
  RETURN 'QF-' || LPAD(next_seq::text, 3, '0');
END;
$$ LANGUAGE plpgsql;
```

Then call it as an RPC: `db.client.Rpc("next_quote_number", "", map[string]interface{}{"p_user_id": userID})`.

---

### BUG 6 — `DeleteClient` will return 500 for clients with quotes

**File:** `internal/handlers/handlers.go` — `DeleteClient`  
**Problem:** `quotes.client_id` has `ON DELETE RESTRICT`. Deleting a client who has quotes triggers a Postgres FK violation. The handler returns a generic 500 instead of a helpful error.  
**Fix:** Catch the FK error and return a `409 Conflict`:

```go
if err := h.db.DeleteClient(r.Context(), id, user.ID); err != nil {
    if strings.Contains(err.Error(), "foreign key") {
        h.err(w, http.StatusConflict, "cannot delete client with existing quotes — delete quotes first")
        return
    }
    h.err(w, http.StatusInternalServerError, "failed to delete client")
    return
}
```

---

### BUG 7 — Notification goroutines capture request context

**File:** `internal/handlers/handlers.go` — `PublicGetQuote` and `PublicAcceptQuote`  
**Problem:** Goroutines for view tracking and notifications use `r.Context()`, but this context is cancelled when the HTTP request returns. The goroutine may fail mid-execution.

```go
go func() {
    _ = h.db.IncrementViewCount(r.Context(), quote.ID)  // ← r.Context() already done
}()
```

**Fix:** Use `context.Background()` or `context.WithTimeout(context.Background(), 15*time.Second)` inside goroutines that outlive the request.

---

## 11. Missing Features — Not Implemented Yet

These are features the architecture was designed to support (models exist, DB columns exist) but the handlers/logic was not written:

### 11.1 — `PATCH /quotes/:id` (Edit Quote)

**Status:** Model `UpdateQuoteRequest` exists in `models.go`. No handler written.  
**What's needed:**

- Handler `UpdateQuote` in `handlers.go`
- Repository method `UpdateQuote(ctx, id, userID string, req *models.UpdateQuoteRequest) error`
- Route: `r.Patch("/{id}", h.UpdateQuote)` in `main.go`
- Logic: Patch only non-nil fields. If line items are included, delete all existing items and re-insert.
- Recalculate totals after line item changes.

---

### 11.2 — Logo Upload

**Status:** `profiles.logo_url` column exists. No upload endpoint.  
**What's needed:**

- `POST /profile/logo` — accepts `multipart/form-data`, uploads to Supabase Storage, returns public URL
- Supabase Storage bucket `logos` with public access policy
- Go: Use Supabase Storage HTTP API or `supabase-go` storage client
- Frontend: Logo upload in Settings → Profile panel already has the UI

---

### 11.3 — Quote PDF Generation

**Status:** Designed but not implemented.  
**What's needed:**

- `GET /quotes/:id/pdf` — generates a PDF of the quote
- Library options: `github.com/go-pdf/fpdf`, `github.com/jung-kurt/gofpdf`, or headless Chrome via `chromedp`
- Most practical: generate HTML (same as the public viewer) and convert with `wkhtmltopdf` or use a PDF service like PDFShift/DocRaptor via API
- Frontend: "Download PDF" button in QuotePreviewModal

---

### 11.4 — Invoice Conversion

**Status:** Not designed yet. One of the most-requested freelancer features.  
**What's needed:**

- New table: `invoices` (mirrors quotes structure but with invoice number, due date, paid_at)
- `POST /quotes/:id/convert-to-invoice` — creates invoice from accepted quote
- `GET /invoices`, `GET /invoices/:id`, `POST /invoices/:id/mark-paid`
- Email notification when invoice is sent

---

### 11.5 — Rate Limiting

**Status:** Not implemented.  
**What's needed:**

- Protect public endpoints (`/q/:token`) from abuse
- Protect auth endpoints from brute force
- Library: `github.com/ulule/limiter` or `golang.org/x/time/rate`
- Free tier enforcement: limit to 3 quote creations per month per user

---

### 11.6 — Free Tier Enforcement

**Status:** The `POST /quotes` endpoint has no quota check.  
**What's needed:**

- Check how many quotes the user has created this calendar month
- If >= 3 and user is on Free plan, return `402 Payment Required` with `{"error": "free_tier_limit", "message": "Upgrade to Pro to create unlimited quotes"}`
- This requires knowing the user's plan — which needs either a `subscriptions` table or checking Stripe

---

### 11.7 — Stripe Integration (Billing)

**Status:** Not implemented. The pricing page and billing settings panel exist on the frontend but are not wired to anything.  
**What's needed:**

- Stripe Checkout for subscription sign-up
- `POST /billing/checkout` — creates a Stripe Checkout session, returns URL
- `POST /billing/portal` — creates a Stripe Customer Portal session
- `POST /webhooks/stripe` — handles `customer.subscription.created/updated/deleted` events
- Store `stripe_customer_id` and `plan` on the `profiles` table
- Protect the free tier limit based on `plan`

---

### 11.8 — Weekly Digest Email (Cron)

**Status:** `notify_weekly` preference stored in profiles. No email sent.  
**What's needed:**

- A weekly cron job (Monday morning) that queries all users with `notify_weekly=true`
- For each user: query their stats for the past week
- Send a summary email via Resend
- Can be implemented as a standalone Go binary in `cmd/cron/` or as a Supabase Edge Function

---

### 11.9 — Expiry Reminder Emails

**Status:** `send_reminder` field stored on quotes. No reminder sent.  
**What's needed:**

- Hourly cron job (or pg_cron function) that finds sent quotes expiring in exactly 3 days and `send_reminder=true`
- Send email to the **client** reminding them the quote expires soon
- Send email to the **freelancer** that a quote is expiring

---

### 11.10 — Quote Revision History

**Status:** Not designed.  
**What's needed:**

- A `quote_revisions` table to track changes over time (useful for disputes)
- Or use `quote_events` more granularly (log diffs)
- `GET /quotes/:id/history` endpoint

---

### 11.11 — Input Validation Library

**Status:** Validation struct tags exist in models (`validate:"required,uuid"`) but **no validation library is wired up**. The tags are decorative only.  
**What's needed:**

- Add `github.com/go-playground/validator/v10` to `go.mod`
- Create a helper in handlers:

```go
var validate = validator.New()

func (h *Handler) validateRequest(v interface{}) error {
    return validate.Struct(v)
}
```

- Call in each handler after decoding: `if err := h.validateRequest(&req); err != nil { h.err(w, 400, err.Error()); return }`

---

## 12. Quick Setup Checklist for Cursor

1. **Fix the module path** — replace `yourusername` in `go.mod` and all `.go` files
2. **Create a Supabase project** at supabase.com
3. **Run the migration** — paste `db/migrations/001_init.sql` into Supabase SQL Editor and run
4. **Set up pg_cron** — in Supabase SQL Editor: `SELECT cron.schedule('expire-quotes','0 * * * *','SELECT expire_old_quotes()');`
5. **Copy env file** — `cp .env.example .env` and fill in all values
6. **Critical:** Set `JWT_SECRET` to match exactly what's in Supabase → Project Settings → API → JWT Secret
7. **Run:** `make run` or `go run cmd/server/main.go`
8. **Test health:** `curl http://localhost:8080/health`

---

## 13. Module Dependencies (go.mod)

```
github.com/go-chi/chi/v5 v5.0.12
github.com/go-chi/cors v1.2.1
github.com/go-chi/jwtauth/v5 v5.3.1
github.com/joho/godotenv v1.5.1
github.com/supabase-community/supabase-go v0.0.4
```

**Still needs to be added:**

- `github.com/go-playground/validator/v10` — input validation
- `golang.org/x/time` — rate limiting (optional)
- `github.com/stripe/stripe-go/v76` — billing (when implementing Stripe)

---

## 14. Deployment Notes

The `Dockerfile` builds a multi-stage image:

1. Stage 1: `golang:1.22-alpine` — compiles the binary with `CGO_ENABLED=0`
2. Stage 2: `scratch` — copies only the binary + CA certificates → ~8MB final image

Recommended platforms: **Railway** (easiest) or **Fly.io**. Both support Dockerfile deploys and managed environment variables.

The frontend deploys separately to **Vercel** or **Netlify**.

CORS must be configured with the production frontend URL in `ALLOWED_ORIGINS`.

---

*Document generated from source: `quoteflow-backend/` — all code was read directly.*
