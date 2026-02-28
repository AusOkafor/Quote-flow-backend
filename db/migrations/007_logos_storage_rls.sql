-- Ensure logos bucket has all required RLS policies (fixes 403 on upload)
-- Upsert needs SELECT + INSERT + UPDATE; public bucket needs public read.

-- Allow users to select their own logo (required for upsert)
DROP POLICY IF EXISTS "Users can select own logo" ON storage.objects;
CREATE POLICY "Users can select own logo"
ON storage.objects FOR SELECT
TO authenticated
USING (
  bucket_id = 'logos' AND
  (storage.foldername(name))[1] = (auth.uid()::text)
);

-- Public read for logos bucket (bucket is public)
DROP POLICY IF EXISTS "Public read logos" ON storage.objects;
CREATE POLICY "Public read logos"
ON storage.objects FOR SELECT
TO public
USING (bucket_id = 'logos');
