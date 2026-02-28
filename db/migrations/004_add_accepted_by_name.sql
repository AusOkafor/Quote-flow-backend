-- Store the signer's name when a client accepts a quote (e-signature)
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS accepted_by_name TEXT;
