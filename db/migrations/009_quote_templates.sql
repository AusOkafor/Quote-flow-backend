-- ============================================================
-- Quote Templates — save quote structure for reuse
-- ============================================================

-- quote_templates — mirrors CreateQuoteRequest fields + name, user_id
CREATE TABLE IF NOT EXISTS quote_templates (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id             UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    title               TEXT NOT NULL DEFAULT '',
    currency            TEXT NOT NULL DEFAULT 'JMD' CHECK (currency IN ('JMD','USD','TTD','BBD')),
    validity_days       INT NOT NULL DEFAULT 14,
    notes               TEXT NOT NULL DEFAULT '',
    deposit             TEXT NOT NULL DEFAULT '',
    payment_method      TEXT NOT NULL DEFAULT '',
    delivery_timeline   TEXT NOT NULL DEFAULT '',
    revisions           TEXT NOT NULL DEFAULT '',
    tax_exempt          BOOLEAN NOT NULL DEFAULT TRUE,
    tax_rate            NUMERIC(5,2) NOT NULL DEFAULT 15.00,
    require_signature   BOOLEAN NOT NULL DEFAULT TRUE,
    track_views         BOOLEAN NOT NULL DEFAULT TRUE,
    send_reminder       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_quote_templates_user_id ON quote_templates(user_id);

-- template_line_items — line items for each template
CREATE TABLE IF NOT EXISTS template_line_items (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    template_id UUID NOT NULL REFERENCES quote_templates(id) ON DELETE CASCADE,
    position    INT NOT NULL DEFAULT 0,
    description TEXT NOT NULL,
    quantity    NUMERIC(10,2) NOT NULL DEFAULT 1,
    unit_price  NUMERIC(12,2) NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_template_line_items_template_id ON template_line_items(template_id);

-- RLS
ALTER TABLE quote_templates ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own templates"
    ON quote_templates FOR ALL
    USING (auth.uid() = user_id);

ALTER TABLE template_line_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Users manage own template line items"
    ON template_line_items FOR ALL
    USING (
        EXISTS (
            SELECT 1 FROM quote_templates t
            WHERE t.id = template_line_items.template_id
              AND t.user_id = auth.uid()
        )
    );
