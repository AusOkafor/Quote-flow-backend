-- Weekly digest: function to get users with notify_weekly = true
-- Returns user_id, email, first_name (from raw_user_meta_data or business_name), business_name, currency

CREATE OR REPLACE FUNCTION get_users_for_weekly_digest()
RETURNS TABLE (
  user_id uuid,
  email text,
  first_name text,
  business_name text,
  currency text
) AS $$
  SELECT
    u.id,
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
$$ LANGUAGE sql SECURITY DEFINER;
