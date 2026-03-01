-- Teams and team members (Business plan feature)
CREATE TABLE IF NOT EXISTS teams (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name        TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS team_members (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id     UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(team_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_team_members_team_id ON team_members(team_id);
CREATE INDEX IF NOT EXISTS idx_team_members_user_id ON team_members(user_id);

-- Add team_id to profiles, quotes, clients
ALTER TABLE profiles ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES teams(id) ON DELETE SET NULL;
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES teams(id) ON DELETE SET NULL;
ALTER TABLE clients ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES teams(id) ON DELETE SET NULL;

-- Backfill: create personal team per existing user
DO $$
DECLARE
  rec RECORD;
  tid UUID;
BEGIN
  FOR rec IN SELECT p.user_id, COALESCE(NULLIF(TRIM(p.business_name), ''), 'My Team') AS team_name
    FROM profiles p
    WHERE p.team_id IS NULL
  LOOP
    INSERT INTO teams (name) VALUES (rec.team_name) RETURNING id INTO tid;
    INSERT INTO team_members (team_id, user_id, role) VALUES (tid, rec.user_id, 'owner');
    UPDATE profiles SET team_id = tid WHERE user_id = rec.user_id;
    UPDATE quotes SET team_id = tid WHERE user_id = rec.user_id;
    UPDATE clients SET team_id = tid WHERE user_id = rec.user_id;
  END LOOP;
END $$;

-- Update client_summary view to include team_id for filtering
-- Must DROP first: CREATE OR REPLACE cannot change column order/names
DROP VIEW IF EXISTS client_summary CASCADE;
CREATE VIEW client_summary AS
SELECT
    c.id,
    c.user_id,
    c.team_id,
    c.name,
    c.company,
    c.email,
    c.phone,
    c.address,
    c.notes,
    c.created_at,
    c.updated_at,
    COUNT(q.id)                                                 AS quote_count,
    COALESCE(SUM(q.total), 0)                                   AS total_quoted,
    COALESCE(
        ROUND(
            COUNT(CASE WHEN q.status = 'accepted' THEN 1 END)::numeric /
            NULLIF(COUNT(CASE WHEN q.status != 'draft' THEN 1 END), 0) * 100,
            0
        ), 0
    )                                                           AS acceptance_rate
FROM clients c
LEFT JOIN quotes q ON q.client_id = c.id
GROUP BY c.id;

-- Update handle_new_user to create team for new signups
CREATE OR REPLACE FUNCTION handle_new_user()
RETURNS TRIGGER AS $$
DECLARE tid UUID;
BEGIN
  INSERT INTO teams (name) VALUES ('My Team') RETURNING id INTO tid;
  INSERT INTO team_members (team_id, user_id, role) VALUES (tid, NEW.id, 'owner');
  INSERT INTO profiles (user_id, email_on_quote, team_id)
  VALUES (NEW.id, COALESCE(NEW.email, ''), tid);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public;
