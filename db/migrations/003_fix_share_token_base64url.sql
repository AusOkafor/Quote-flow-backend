-- Fix: base64url encoding not supported in PostgreSQL < 19 (Supabase uses 15/16).
-- Use base64 with URL-safe character replacement instead.
ALTER TABLE quotes
  ALTER COLUMN share_token SET DEFAULT
  replace(replace(encode(gen_random_bytes(24), 'base64'), '+', '-'), '/', '_');
