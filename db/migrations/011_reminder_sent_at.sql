-- Add reminder_sent_at to track when client reminder was sent (avoid duplicates)
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS reminder_sent_at TIMESTAMPTZ;
