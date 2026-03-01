-- Upgrade a user's account to Business plan for testing
-- Run this in Supabase SQL Editor: https://supabase.com/dashboard/project/_/sql
--
-- Option A: By email (replace with your email)
UPDATE profiles
SET plan = 'business'
WHERE user_id = (
  SELECT id FROM auth.users WHERE email = 'YOUR_EMAIL@example.com'
);

-- Option B: By user ID (replace with your UUID from Auth → Users)
-- UPDATE profiles SET plan = 'business' WHERE user_id = 'your-uuid-here';
