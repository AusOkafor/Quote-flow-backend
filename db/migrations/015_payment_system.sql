-- ── PAYMENT ACCOUNTS ──────────────────────────────────────────────────────────
-- One row per processor per freelancer. Stores credentials (encrypted at rest).

CREATE TABLE IF NOT EXISTS payment_accounts (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id               UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  processor             TEXT NOT NULL CHECK (processor IN ('wipay', 'stripe', 'paypal')),

  -- WiPay
  wipay_account_id      TEXT,
  wipay_api_key         TEXT,

  -- Stripe Connect
  stripe_account_id     TEXT,
  stripe_access_token   TEXT,

  -- PayPal Commerce Platform
  paypal_merchant_id    TEXT,
  paypal_access_token   TEXT,

  is_active             BOOLEAN NOT NULL DEFAULT TRUE,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  UNIQUE (user_id, processor)
);

ALTER TABLE payment_accounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own payment accounts"
  ON payment_accounts FOR ALL
  USING (auth.uid() = user_id);


-- ── PAYMENTS ──────────────────────────────────────────────────────────────────
-- One row per transaction attempt. Created when payment link is generated.
-- Updated to 'paid' when webhook confirms.

CREATE TABLE IF NOT EXISTS payments (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  quote_id              UUID NOT NULL REFERENCES quotes(id) ON DELETE RESTRICT,
  user_id               UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,

  processor             TEXT NOT NULL CHECK (processor IN ('wipay', 'stripe', 'paypal')),
  payment_type          TEXT NOT NULL CHECK (payment_type IN ('full', 'deposit', 'balance')),

  amount                NUMERIC(12,2) NOT NULL,
  platform_fee          NUMERIC(12,2) NOT NULL,
  net_amount            NUMERIC(12,2) NOT NULL,

  currency              TEXT NOT NULL,

  status                TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'paid', 'failed', 'refunded')),

  processor_payment_id  TEXT,
  payment_url           TEXT,
  paid_at               TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payments_quote_id ON payments(quote_id);
CREATE INDEX IF NOT EXISTS idx_payments_user_id ON payments(user_id);
CREATE INDEX IF NOT EXISTS idx_payments_user_status ON payments(user_id, status);

ALTER TABLE payments ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own payments"
  ON payments FOR ALL
  USING (auth.uid() = user_id);


-- ── PROFILE ADDITIONS ─────────────────────────────────────────────────────────

ALTER TABLE profiles ADD COLUMN IF NOT EXISTS
  default_payment_timing TEXT NOT NULL DEFAULT 'link_only'
  CHECK (default_payment_timing IN ('full', 'deposit', 'link_only'));

ALTER TABLE profiles ADD COLUMN IF NOT EXISTS
  preferred_usd_processor TEXT
  CHECK (preferred_usd_processor IN ('stripe', 'paypal'));


-- ── QUOTE ADDITIONS ───────────────────────────────────────────────────────────

ALTER TABLE quotes ADD COLUMN IF NOT EXISTS deposit_paid_at  TIMESTAMPTZ;
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS fully_paid_at    TIMESTAMPTZ;
