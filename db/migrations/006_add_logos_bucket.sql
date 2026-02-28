-- Logo upload: create storage bucket and RLS policies
-- Run in Supabase SQL Editor. If bucket creation fails, create "logos" bucket manually in Dashboard (Storage → New bucket, public, 2MB limit).

INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
VALUES (
  'logos',
  'logos',
  true,
  2097152,
  ARRAY['image/png', 'image/jpeg', 'image/jpg', 'image/svg+xml']
)
ON CONFLICT (id) DO NOTHING;

-- Allow authenticated users to upload to their own folder (logos/{user_id}/...)
DROP POLICY IF EXISTS "Users can upload own logo" ON storage.objects;
CREATE POLICY "Users can upload own logo"
ON storage.objects FOR INSERT
TO authenticated
WITH CHECK (
  bucket_id = 'logos' AND
  (storage.foldername(name))[1] = (auth.uid()::text)
);

-- Allow users to select their own logo (required for upsert)
DROP POLICY IF EXISTS "Users can select own logo" ON storage.objects;
CREATE POLICY "Users can select own logo"
ON storage.objects FOR SELECT
TO authenticated
USING (
  bucket_id = 'logos' AND
  (storage.foldername(name))[1] = (auth.uid()::text)
);

-- Allow users to update/delete their own logo
DROP POLICY IF EXISTS "Users can update own logo" ON storage.objects;
CREATE POLICY "Users can update own logo"
ON storage.objects FOR UPDATE
TO authenticated
USING (bucket_id = 'logos' AND (storage.foldername(name))[1] = (auth.uid()::text));

DROP POLICY IF EXISTS "Users can delete own logo" ON storage.objects;
CREATE POLICY "Users can delete own logo"
ON storage.objects FOR DELETE
TO authenticated
USING (bucket_id = 'logos' AND (storage.foldername(name))[1] = (auth.uid()::text));

-- Public read for logos bucket (bucket is public)
DROP POLICY IF EXISTS "Public read logos" ON storage.objects;
CREATE POLICY "Public read logos"
ON storage.objects FOR SELECT
TO public
USING (bucket_id = 'logos');
