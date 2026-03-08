-- Fix get_users_for_weekly_digest to return a proper JSON array (not object).
-- PostgREST returns TABLE/SETOF as array; ensure function signature is correct.
-- Use user_id (not id) to match DigestUser model json:"user_id".

DROP FUNCTION IF EXISTS get_users_for_weekly_digest();

CREATE OR REPLACE FUNCTION get_users_for_weekly_digest()
RETURNS TABLE (
    user_id       uuid,
    email         text,
    first_name    text,
    business_name text,
    currency      text
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT
    u.id AS user_id,
    u.email::text,
    COALESCE(
      trim(split_part(COALESCE(u.raw_user_meta_data->>'full_name', p.business_name), ' ', 1)),
      split_part(u.email::text, '@', 1),
      'there'
    ),
    COALESCE(p.business_name, ''),
    COALESCE(p.default_currency, 'JMD')
  FROM profiles p
  JOIN auth.users u ON u.id = p.user_id
  WHERE p.notify_weekly = true
$$;
