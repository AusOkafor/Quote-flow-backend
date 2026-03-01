-- Allow 'business' plan in profiles
DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'profiles'::regclass AND contype = 'c'
    AND pg_get_constraintdef(oid) LIKE '%plan%'
  LOOP
    EXECUTE format('ALTER TABLE profiles DROP CONSTRAINT %I', r.conname);
  END LOOP;
END $$;
ALTER TABLE profiles ADD CONSTRAINT profiles_plan_check CHECK (plan IN ('free', 'pro', 'business'));
