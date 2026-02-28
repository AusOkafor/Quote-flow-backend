# Email Setup (Resend)

QuoteFlow sends quote links to clients via email using [Resend](https://resend.com). Follow these steps to enable it.

## Quick setup (testing)

1. **Sign up** at [resend.com](https://resend.com) (free tier: 3,000 emails/month).

2. **Create an API key**
   - Go to [resend.com/api-keys](https://resend.com/api-keys)
   - Click **Create API Key**
   - Name it (e.g. `QuoteFlow dev`)
   - Copy the key (starts with `re_`) — you can only see it once

3. **Add to your `.env`** in `quoteflow-backend/`:
   ```
   RESEND_API_KEY=re_xxxxxxxxxxxxxxxx
   EMAIL_FROM=onboarding@resend.dev
   EMAIL_FROM_NAME=QuoteFlow
   ```

4. **Restart the backend** so it picks up the new env vars.

The `onboarding@resend.dev` address works for testing without verifying your own domain. Emails will send to real addresses (your clients).

## Production setup

For production, use your own domain so emails come from `quotes@yourdomain.com`:

1. Go to [resend.com/domains](https://resend.com/domains)
2. Click **Add Domain** and enter your domain (e.g. `yourdomain.com` or `quotes.yourdomain.com`)
3. Add the DNS records Resend provides (SPF + DKIM) to your domain’s DNS
4. Wait for verification (usually 5–10 minutes)
5. Update `.env`:
   ```
   EMAIL_FROM=quotes@yourdomain.com
   EMAIL_FROM_NAME=Your Business Name
   ```

## Troubleshooting

- **"RESEND_API_KEY not configured"** — Add the key to `.env` and restart the server.
- **422 / domain error** — For testing, use `onboarding@resend.dev`. For production, ensure your domain is verified.
- **Emails not arriving** — Check spam. Resend has good deliverability; using your own verified domain improves it.
