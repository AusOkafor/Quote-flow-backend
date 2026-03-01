-- ============================================================
-- Add note_type to quote_notes for "Request Changes" flow
-- ============================================================

ALTER TABLE quote_notes
  ADD COLUMN IF NOT EXISTS note_type TEXT NOT NULL DEFAULT 'message';

-- Enforce valid values (idempotent: drop first if re-running)
ALTER TABLE quote_notes DROP CONSTRAINT IF EXISTS quote_notes_note_type_check;
ALTER TABLE quote_notes ADD CONSTRAINT quote_notes_note_type_check
  CHECK (note_type IN ('message', 'change_request'));
