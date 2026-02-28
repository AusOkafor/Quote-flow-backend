-- Add paid_at to quotes (set when freelancer marks an accepted quote as paid)
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;
