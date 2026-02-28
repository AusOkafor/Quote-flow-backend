# QuoteFlow — Backend API

Go + Supabase backend for the QuoteFlow quote-building SaaS.

---

## Stack

| Layer | Tech |
|---|---|
| Language | Go 1.22 |
| Router | Chi v5 |
| Database | Supabase (Postgres + Auth + Storage) |
| Auth | Supabase Auth (JWT) — verified server-side |
| Email | Resend (raw HTTP, no SDK dep) |
| WhatsApp | Twilio WhatsApp Business API |
| Deploy | Docker / Railway / Fly.io / Render |

---

## Project Structure

```
quoteflow-backend/
├── cmd/
│   └── server/
│       └── main.go              # Entry point + router
├── config/
│   └── config.go                # Env var loader
├── db/
│   └── migrations/
│       └── 001_init.sql         # All Supabase SQL (run once)
├── internal/
│   ├── handlers/
│   │   └── handlers.go          # All HTTP handlers
│   ├── middleware/
│   │   └── auth.go              # JWT verification middleware
│   ├── models/
│   │   └── models.go            # All domain types + request/response structs
│   ├── repository/
│   │   └── db.go                # All Supabase data access
│   └── services/
│       ├── export.go            # CSV generation
│       └── notifications.go     # Email (Resend) + WhatsApp (Twilio)
├── .env.example                 # Copy to .env and fill in
├── .air.toml                    # Hot-reload config
├── Dockerfile
└── Makefile
```

---

## Quick Start

### 1. Clone and install deps

```bash
git clone <your-repo>
cd quoteflow-backend
make tidy          # go mod tidy + verify
make env           # copies .env.example → .env
```

### 2. Set up Supabase

1. Create a project at [supabase.com](https://supabase.com)
2. Go to **SQL Editor** and run the migration:

```bash
# Copy the entire contents of db/migrations/001_init.sql
# and paste into the Supabase SQL Editor, then click Run
```

3. Copy your keys from **Project Settings → API**:
   - `SUPABASE_URL`
   - `SUPABASE_ANON_KEY`
   - `SUPABASE_SERVICE_ROLE_KEY` ← server-side only, never expose to frontend

4. Copy the connection string from **Project Settings → Database → Connection string (URI)**:
   - `SUPABASE_DB_URL`

### 3. Configure environment

Edit `.env`:

```env
PORT=8080
ENV=development

SUPABASE_URL=https://xxxx.supabase.co
SUPABASE_ANON_KEY=eyJ...
SUPABASE_SERVICE_ROLE_KEY=eyJ...
SUPABASE_DB_URL=postgresql://postgres:PASSWORD@db.xxxx.supabase.co:5432/postgres

JWT_SECRET=change-this-to-a-random-32+-char-string

# Email — sign up free at resend.com (3,000 emails/month free)
RESEND_API_KEY=re_...
EMAIL_FROM=quotes@yourdomain.com
EMAIL_FROM_NAME=QuoteFlow

# WhatsApp — sign up at twilio.com (free trial with sandbox number)
TWILIO_ACCOUNT_SID=AC...
TWILIO_AUTH_TOKEN=...
TWILIO_WHATSAPP_NUMBER=whatsapp:+14155238886

APP_URL=http://localhost:8080
FRONTEND_URL=http://localhost:5173
QUOTE_LINK_BASE_URL=http://localhost:8080/q

ALLOWED_ORIGINS=http://localhost:5173,http://localhost:3000
```

### 4. Run

```bash
# Development with hot-reload
make dev

# Or without hot-reload
make run

# Production binary
make build
./bin/quoteflow
```

---

## All API Endpoints

### Authentication
Auth (register/login) is handled by **Supabase Auth directly from the frontend**.  
The backend only validates the JWT Supabase issues.

**Frontend flow:**
```js
// Sign up / login via Supabase JS client
const { data } = await supabase.auth.signInWithPassword({ email, password })
// data.session.access_token  ← pass this as Authorization: Bearer <token>
```

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | ❌ | Server health check |
| GET | `/auth/me` | ✅ | Current user + profile |

---

### Dashboard

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/dashboard` | ✅ | Stats: total quoted, acceptance rate, avg value, activity feed |

**Response:**
```json
{
  "success": true,
  "data": {
    "total_quoted_this_month": 482000,
    "quoted_change_percent": 18,
    "quotes_accepted_this_month": 7,
    "acceptance_rate": 64.0,
    "avg_quote_value": 68857,
    "draft_count": 2,
    "sent_count": 4,
    "accepted_count": 7,
    "expired_count": 1,
    "recent_activity": [...]
  }
}
```

---

### Profile / Settings

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/profile` | ✅ | Get user's profile + settings |
| PUT | `/profile` | ✅ | Update all profile fields |

**PUT /profile body:**
```json
{
  "business_name": "Karl McKenzie Creative",
  "profession": "Graphic Designer",
  "address": "Kingston 10, Jamaica",
  "phone": "+1 (876) 555-0128",
  "email_on_quote": "karl@kmc.jm",
  "brand_color": "#E85C2F",
  "default_currency": "JMD",
  "default_validity_days": 14,
  "default_deposit": "50% upfront",
  "tax_type": "GCT",
  "tax_rate": 15.00,
  "tax_exempt_default": true,
  "notify_accepted": true,
  "notify_viewed": true,
  "notify_expiring": true,
  "notify_weekly": false
}
```

---

### Clients

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/clients` | ✅ | List all clients with quote stats |
| POST | `/clients` | ✅ | Create a new client |
| GET | `/clients/:id` | ✅ | Get single client |
| PUT | `/clients/:id` | ✅ | Update client |
| DELETE | `/clients/:id` | ✅ | Delete client |

**POST /clients body:**
```json
{
  "name": "Simone Richards",
  "company": "Richards Design Co.",
  "email": "simone@gmail.com",
  "phone": "+1 (876) 555-0192",
  "address": "Kingston 6, Jamaica",
  "notes": "Prefers WhatsApp communication"
}
```

**GET /clients response (includes computed stats):**
```json
{
  "success": true,
  "data": [
    {
      "id": "uuid",
      "name": "Simone Richards",
      "email": "simone@gmail.com",
      "quote_count": 7,
      "total_quoted": 340000,
      "acceptance_rate": 86
    }
  ]
}
```

---

### Quotes

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/quotes` | ✅ | List quotes (`?status=all\|draft\|sent\|accepted\|expired`) |
| POST | `/quotes` | ✅ | Create quote with line items |
| GET | `/quotes/export` | ✅ | Download all quotes as `.csv` |
| GET | `/quotes/:id` | ✅ | Get quote with client + line items |
| DELETE | `/quotes/:id` | ✅ | Delete a quote |
| POST | `/quotes/:id/send` | ✅ | Send via email, WhatsApp, or get link |
| POST | `/quotes/:id/duplicate` | ✅ | Clone quote as new draft |

**POST /quotes body:**
```json
{
  "client_id": "uuid",
  "title": "Brand Identity Design — March 2026",
  "currency": "JMD",
  "validity_days": 14,
  "tax_exempt": true,
  "tax_rate": 15.0,
  "deposit": "50% upfront",
  "payment_method": "Bank Transfer",
  "delivery_timeline": "10 business days after deposit",
  "revisions": "2 rounds",
  "notes": "Files delivered via Google Drive upon final payment.",
  "require_signature": true,
  "track_views": true,
  "send_reminder": false,
  "line_items": [
    { "description": "Brand Logo Design", "quantity": 1, "unit_price": 40000 },
    { "description": "Brand Guidelines",  "quantity": 1, "unit_price": 25000 },
    { "description": "Business Card",     "quantity": 1, "unit_price": 15000 }
  ]
}
```

**POST /quotes/:id/send body:**
```json
{
  "channel": "email",
  "recipient_email": "client@email.com",
  "message": "Hi Simone, please find your quote attached."
}
```
```json
{
  "channel": "whatsapp",
  "recipient_phone": "+18765550192"
}
```
```json
{
  "channel": "link"
}
```

**Send response:**
```json
{
  "success": true,
  "data": {
    "message": "quote sent successfully",
    "quote_link": "http://localhost:8080/q/abc123token",
    "channel": "email"
  }
}
```

---

### Public Quote Viewer (no auth — for clients)

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/q/:token` | ❌ | Load quote for client viewer, increments view count |
| POST | `/q/:token/accept` | ❌ | Client accepts the quote, notifies freelancer |

**POST /q/:token/accept body:**
```json
{
  "signature_name": "Simone Richards"
}
```

---

## Frontend Integration

### Setting the auth header
```js
// After Supabase login:
const token = supabase.auth.session()?.access_token

// Pass on every API call:
fetch('http://localhost:8080/quotes', {
  headers: { 'Authorization': `Bearer ${token}` }
})
```

### Calling send quote
```js
// Send via WhatsApp
await fetch(`/quotes/${quoteId}/send`, {
  method: 'POST',
  headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({ channel: 'whatsapp', recipient_phone: client.phone })
})

// Download CSV — trigger browser download
window.location.href = `http://localhost:8080/quotes/export?token=${token}`
// Or via fetch + blob:
const res = await fetch('/quotes/export', { headers: { Authorization: `Bearer ${token}` } })
const blob = await res.blob()
const url = URL.createObjectURL(blob)
const a = document.createElement('a'); a.href = url; a.download = 'quotes.csv'; a.click()
```

### Public quote link
```
http://yourapp.com/q/SHARE_TOKEN
```
This URL is safe to share via WhatsApp or email — no login needed.  
It calls `GET /q/:token` to load the quote and `POST /q/:token/accept` when the client taps Accept.

---

## Environment Setup: Email & WhatsApp

### Email (Resend) — Free: 3,000/month
1. Sign up at [resend.com](https://resend.com)
2. Add and verify your domain (takes 5 minutes)
3. Create an API key → paste into `RESEND_API_KEY`
4. Set `EMAIL_FROM` to `quotes@yourdomain.com`

### WhatsApp (Twilio) — Free sandbox for testing
1. Sign up at [twilio.com](https://twilio.com)
2. Go to **Messaging → Try WhatsApp Sandbox**
3. Follow the instructions to join your sandbox
4. Copy `TWILIO_ACCOUNT_SID` and `TWILIO_AUTH_TOKEN`
5. `TWILIO_WHATSAPP_NUMBER=whatsapp:+14155238886` (sandbox number)

> For production WhatsApp, apply for a Twilio WhatsApp Business sender.

---

## Deploy to Railway (recommended for MVP)

```bash
# Install Railway CLI
npm i -g @railway/cli
railway login

# Create project and deploy
railway init
railway up

# Set environment variables in Railway dashboard
# or via CLI:
railway vars set SUPABASE_URL=https://xxx.supabase.co
railway vars set JWT_SECRET=your-secret
# ... etc
```

---

## Cron: Auto-expire quotes

In Supabase SQL Editor, enable `pg_cron` and schedule the expiry job:

```sql
-- Enable pg_cron (once, in Supabase dashboard → Extensions)
-- Then:
SELECT cron.schedule(
  'expire-quotes',
  '0 * * * *',           -- every hour
  'SELECT expire_old_quotes()'
);
```

---

## Running Tests

```bash
make test
make test-cover   # generates coverage.html
```
