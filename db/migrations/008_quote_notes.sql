-- ============================================================
-- Quote Notes Thread
-- Lightweight comment thread attached to each quote
-- ============================================================

CREATE TABLE IF NOT EXISTS quote_notes (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id     UUID NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
    author_type  TEXT NOT NULL CHECK (author_type IN ('client', 'freelancer')),
    author_name  TEXT NOT NULL,
    message      TEXT NOT NULL,
    read_at      TIMESTAMPTZ,  -- when freelancer viewed (null = unread client note)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_quote_notes_quote_id ON quote_notes(quote_id);
CREATE INDEX IF NOT EXISTS idx_quote_notes_created ON quote_notes(quote_id, created_at);

-- RLS: backend uses service role (bypasses). For direct client access we'd need policies.
-- Public access to notes is via backend API only.
ALTER TABLE quote_notes ENABLE ROW LEVEL SECURITY;

-- Allow service role full access (backend)
CREATE POLICY "Service role full access"
    ON quote_notes FOR ALL
    USING (true)
    WITH CHECK (true);
