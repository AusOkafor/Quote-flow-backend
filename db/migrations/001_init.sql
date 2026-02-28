-- ============================================================
-- QuoteFlow — Supabase Database Migrations
-- Run these in order in your Supabase SQL Editor
-- ============================================================

-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ────────────────────────────────────────────────────────────
-- TABLE: profiles
-- One row per user. Linked to Supabase Auth via user_id.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS profiles (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id             UUID NOT NULL UNIQUE REFERENCES auth.users(id) ON DELETE CASCADE,
    business_name       TEXT NOT NULL DEFAULT '',
    profession          TEXT NOT NULL DEFAULT '',
    address             TEXT NOT NULL DEFAULT '',
    phone               TEXT NOT NULL DEFAULT '',
    email_on_quote      TEXT NOT NULL DEFAULT '',
    logo_url            TEXT,
    brand_color         TEXT NOT NULL DEFAULT '#E85C2F',
    -- Quote defaults
    default_currency    TEXT NOT NULL DEFAULT 'JMD',
    default_validity_days INT NOT NULL DEFAULT 14,
    default_deposit     TEXT NOT NULL DEFAULT '50% upfront',
    default_revisions   TEXT NOT NULL DEFAULT '2 rounds',
    default_notes       TEXT NOT NULL DEFAULT '',
    default_payment     TEXT NOT NULL DEFAULT '',
    -- Tax
    tax_type            TEXT NOT NULL DEFAULT 'GCT',
    tax_rate            NUMERIC(5,2) NOT NULL DEFAULT 15.00,
    tax_number          TEXT NOT NULL DEFAULT '',
    tax_exempt_default  BOOLEAN NOT NULL DEFAULT TRUE,
    show_tax_breakdown  BOOLEAN NOT NULL DEFAULT TRUE,
    -- Billing / subscription
    plan                TEXT NOT NULL DEFAULT 'free' CHECK (plan IN ('free', 'pro')),
    stripe_customer_id  TEXT,
    -- Notifications
    notify_accepted     BOOLEAN NOT NULL DEFAULT TRUE,
    notify_viewed       BOOLEAN NOT NULL DEFAULT TRUE,
    notify_expiring     BOOLEAN NOT NULL DEFAULT TRUE,
    notify_weekly       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ────────────────────────────────────────────────────────────
-- TABLE: clients
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS clients (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    company    TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL,
    phone      TEXT NOT NULL DEFAULT '',
    address    TEXT NOT NULL DEFAULT '',
    notes      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_clients_user_id ON clients(user_id);

-- ────────────────────────────────────────────────────────────
-- TABLE: quotes
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS quotes (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id             UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    client_id           UUID NOT NULL REFERENCES clients(id) ON DELETE RESTRICT,
    quote_number        TEXT NOT NULL,         -- e.g. "QF-019"
    title               TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft','sent','accepted','expired','declined')),
    currency            TEXT NOT NULL DEFAULT 'JMD'
                            CHECK (currency IN ('JMD','USD','TTD','BBD')),
    subtotal            NUMERIC(12,2) NOT NULL DEFAULT 0,
    tax_rate            NUMERIC(5,2) NOT NULL DEFAULT 15.00,
    tax_exempt          BOOLEAN NOT NULL DEFAULT TRUE,
    tax_amount          NUMERIC(12,2) NOT NULL DEFAULT 0,
    total               NUMERIC(12,2) NOT NULL DEFAULT 0,
    validity_days       INT NOT NULL DEFAULT 14,
    expires_at          TIMESTAMPTZ NOT NULL,
    notes               TEXT NOT NULL DEFAULT '',
    -- Terms
    deposit             TEXT NOT NULL DEFAULT '',
    payment_method      TEXT NOT NULL DEFAULT '',
    delivery_timeline   TEXT NOT NULL DEFAULT '',
    revisions           TEXT NOT NULL DEFAULT '',
    -- Options
    require_signature   BOOLEAN NOT NULL DEFAULT TRUE,
    track_views         BOOLEAN NOT NULL DEFAULT TRUE,
    send_reminder       BOOLEAN NOT NULL DEFAULT FALSE,
    -- Tracking
    view_count          INT NOT NULL DEFAULT 0,
    last_viewed_at      TIMESTAMPTZ,
    accepted_at         TIMESTAMPTZ,
    sent_at             TIMESTAMPTZ,
    -- Public share link token (unique per quote)
    share_token         TEXT UNIQUE NOT NULL DEFAULT replace(replace(encode(gen_random_bytes(24), 'base64'), '+', '-'), '/', '_'),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Each user's quote numbers are unique
    UNIQUE (user_id, quote_number)
);

CREATE INDEX IF NOT EXISTS idx_quotes_user_id   ON quotes(user_id);
CREATE INDEX IF NOT EXISTS idx_quotes_client_id ON quotes(client_id);
CREATE INDEX IF NOT EXISTS idx_quotes_status    ON quotes(user_id, status);
CREATE INDEX IF NOT EXISTS idx_quotes_share     ON quotes(share_token);

-- ────────────────────────────────────────────────────────────
-- TABLE: line_items
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS line_items (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id    UUID NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
    position    INT NOT NULL DEFAULT 0,
    description TEXT NOT NULL,
    quantity    NUMERIC(10,2) NOT NULL DEFAULT 1,
    unit_price  NUMERIC(12,2) NOT NULL DEFAULT 0,
    total       NUMERIC(12,2) GENERATED ALWAYS AS (quantity * unit_price) STORED
);

CREATE INDEX IF NOT EXISTS idx_line_items_quote_id ON line_items(quote_id);

-- ────────────────────────────────────────────────────────────
-- TABLE: quote_events  (activity feed)
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS quote_events (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id      UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    quote_id     UUID REFERENCES quotes(id) ON DELETE SET NULL,
    event_type   TEXT NOT NULL,   -- accepted | viewed | expiring | created | sent | duplicated
    message      TEXT NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_events_user_id ON quote_events(user_id);
CREATE INDEX IF NOT EXISTS idx_events_occurred ON quote_events(user_id, occurred_at DESC);

-- ────────────────────────────────────────────────────────────
-- AUTO-UPDATE updated_at TRIGGERS
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_profiles_updated_at
    BEFORE UPDATE ON profiles
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_clients_updated_at
    BEFORE UPDATE ON clients
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_quotes_updated_at
    BEFORE UPDATE ON quotes
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- ────────────────────────────────────────────────────────────
-- AUTO-CREATE PROFILE ON SIGNUP
-- Fires when a new user is created in auth.users
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION handle_new_user()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO profiles (user_id, email_on_quote)
    VALUES (NEW.id, NEW.email);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

CREATE TRIGGER on_auth_user_created
    AFTER INSERT ON auth.users
    FOR EACH ROW EXECUTE FUNCTION handle_new_user();

-- ────────────────────────────────────────────────────────────
-- FUNCTION: auto-expire quotes past their expiry date
-- Call this via a Supabase cron job (pg_cron):
--   SELECT cron.schedule('expire-quotes','0 * * * *','SELECT expire_old_quotes()');
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION expire_old_quotes()
RETURNS void AS $$
BEGIN
    UPDATE quotes
    SET status = 'expired'
    WHERE status = 'sent'
      AND expires_at < NOW();
END;
$$ LANGUAGE plpgsql;

-- ────────────────────────────────────────────────────────────
-- ROW LEVEL SECURITY (RLS)
-- Users can only see their own data
-- ────────────────────────────────────────────────────────────

-- Profiles
ALTER TABLE profiles ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own profile"
    ON profiles FOR ALL
    USING (auth.uid() = user_id);

-- Clients
ALTER TABLE clients ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own clients"
    ON clients FOR ALL
    USING (auth.uid() = user_id);

-- Quotes
ALTER TABLE quotes ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own quotes"
    ON quotes FOR ALL
    USING (auth.uid() = user_id);

-- Line items (access through parent quote)
ALTER TABLE line_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own line items"
    ON line_items FOR ALL
    USING (
        EXISTS (
            SELECT 1 FROM quotes q
            WHERE q.id = line_items.quote_id
              AND q.user_id = auth.uid()
        )
    );

-- Quote events
ALTER TABLE quote_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users see own events"
    ON quote_events FOR ALL
    USING (auth.uid() = user_id);

-- Public quote access via share token (no auth required)
CREATE POLICY "Public can view quotes by share token"
    ON quotes FOR SELECT
    USING (share_token IS NOT NULL);

CREATE POLICY "Public can view line items of shared quotes"
    ON line_items FOR SELECT
    USING (
        EXISTS (
            SELECT 1 FROM quotes q
            WHERE q.id = line_items.quote_id
              AND q.share_token IS NOT NULL
        )
    );

-- ────────────────────────────────────────────────────────────
-- USEFUL VIEWS
-- ────────────────────────────────────────────────────────────

-- Client summary view (quote stats per client)
CREATE OR REPLACE VIEW client_summary AS
SELECT
    c.id,
    c.user_id,
    c.name,
    c.company,
    c.email,
    c.phone,
    c.address,
    c.notes,
    c.created_at,
    c.updated_at,
    COUNT(q.id)                                                 AS quote_count,
    COALESCE(SUM(q.total), 0)                                   AS total_quoted,
    COALESCE(
        ROUND(
            COUNT(CASE WHEN q.status = 'accepted' THEN 1 END)::numeric /
            NULLIF(COUNT(CASE WHEN q.status != 'draft' THEN 1 END), 0) * 100,
            0
        ), 0
    )                                                           AS acceptance_rate
FROM clients c
LEFT JOIN quotes q ON q.client_id = c.id
GROUP BY c.id;

-- ────────────────────────────────────────────────────────────
-- FUNCTION: increment_view_count(quote_id uuid)
-- Called via Supabase RPC to atomically bump view count.
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION increment_view_count(quote_id uuid)
RETURNS void AS $$
BEGIN
    UPDATE quotes
    SET
        view_count     = view_count + 1,
        last_viewed_at = NOW(),
        updated_at     = NOW()
    WHERE id = quote_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- ────────────────────────────────────────────────────────────
-- FUNCTION: next_quote_number(p_user_id uuid)
-- Atomically generates the next sequential quote number.
-- Avoids race conditions by using MAX + 1 in a single query.
-- ────────────────────────────────────────────────────────────
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
