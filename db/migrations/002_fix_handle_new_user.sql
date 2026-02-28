-- Fix handle_new_user trigger: handle null email (phone/OAuth) and set search_path
CREATE OR REPLACE FUNCTION handle_new_user()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO public.profiles (user_id, email_on_quote)
    VALUES (NEW.id, COALESCE(NEW.email, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public;
