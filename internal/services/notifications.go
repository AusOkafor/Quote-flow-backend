package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
)

// NotificationService handles sending emails (Resend) and WhatsApp (Twilio).
type NotificationService struct {
	cfg *config.Config
}

func NewNotificationService(cfg *config.Config) *NotificationService {
	return &NotificationService{cfg: cfg}
}

// ─────────────────────────────────────────────────────────────────────────────
// EMAIL — RESEND
// ─────────────────────────────────────────────────────────────────────────────

type resendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

func (n *NotificationService) sendEmail(to, subject, html string) error {
	if n.cfg.ResendAPIKey == "" {
		return fmt.Errorf("RESEND_API_KEY not configured")
	}

	payload := resendPayload{
		From:    fmt.Sprintf("%s <%s>", n.cfg.EmailFromName, n.cfg.EmailFrom),
		To:      []string{to},
		Subject: subject,
		HTML:    html,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.cfg.ResendAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("resend request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("resend returned %d", resp.StatusCode)
	}
	return nil
}

// SendQuoteByEmail sends the quote link to the specified recipient (or falls back to client email).
func (n *NotificationService) SendQuoteByEmail(quote *models.QuoteWithDetails, recipientEmail, senderName string) error {
	quoteURL := fmt.Sprintf("%s/%s", n.cfg.QuoteLinkBaseURL, quote.ShareToken)
	expiryStr := quote.ExpiresAt.Format("January 2, 2006")

	html := quoteEmailHTML(QuoteEmailData{
		ClientName:  quote.Client.Name,
		SenderName:  senderName,
		QuoteTitle:  quote.Title,
		QuoteNumber: quote.QuoteNumber,
		Total:       fmt.Sprintf("%s %.2f", quote.Currency, quote.Total),
		QuoteURL:    quoteURL,
		ExpiresAt:   expiryStr,
	})

	to := recipientEmail
	if to == "" {
		to = quote.Client.Email
	}

	subject := fmt.Sprintf("Quote %s from %s — %s", quote.QuoteNumber, senderName, quote.Title)
	return n.sendEmail(to, subject, html)
}

// SendQuoteAcceptedNotification notifies the freelancer that a quote was accepted.
func (n *NotificationService) SendQuoteAcceptedNotification(quote *models.QuoteWithDetails, freelancerEmail string) error {
	html := fmt.Sprintf(`
	<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
	  <h2 style="color:#2DAB6F;margin-bottom:8px;">🎉 Quote Accepted!</h2>
	  <p style="color:#555;font-size:15px;line-height:1.6;">
	    <strong>%s</strong> has accepted your quote <strong>%s</strong> for <strong>%s %.2f</strong>.
	  </p>
	  <p style="color:#555;font-size:14px;">Log in to QuoteFlow to convert it to an invoice or follow up.</p>
	  <a href="%s/app" style="display:inline-block;margin-top:20px;background:#E85C2F;color:#fff;padding:13px 28px;border-radius:8px;text-decoration:none;font-weight:600;">
	    View in QuoteFlow →
	  </a>
	</div>`, quote.Client.Name, quote.QuoteNumber, quote.Currency, quote.Total, n.cfg.AppURL)

	subject := fmt.Sprintf("✅ %s accepted your quote %s", quote.Client.Name, quote.QuoteNumber)
	return n.sendEmail(freelancerEmail, subject, html)
}

// SendQuoteViewedNotification notifies the freelancer the quote was opened.
func (n *NotificationService) SendQuoteViewedNotification(quote *models.Quote, clientName, freelancerEmail string) error {
	html := fmt.Sprintf(`
	<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
	  <h2 style="color:#2F7DE8;margin-bottom:8px;">👁 Quote Viewed</h2>
	  <p style="color:#555;font-size:15px;line-height:1.6;">
	    <strong>%s</strong> just opened your quote <strong>%s</strong>.
	    Total views: <strong>%d</strong>.
	  </p>
	  <a href="%s/app" style="display:inline-block;margin-top:20px;background:#0D0D0D;color:#fff;padding:13px 28px;border-radius:8px;text-decoration:none;font-weight:600;">
	    View in QuoteFlow →
	  </a>
	</div>`, clientName, quote.QuoteNumber, quote.ViewCount+1, n.cfg.AppURL)

	subject := fmt.Sprintf("👁 %s viewed your quote %s", clientName, quote.QuoteNumber)
	return n.sendEmail(freelancerEmail, subject, html)
}

// ─────────────────────────────────────────────────────────────────────────────
// WHATSAPP — TWILIO
// ─────────────────────────────────────────────────────────────────────────────

func (n *NotificationService) SendQuoteViaWhatsApp(quote *models.QuoteWithDetails, toPhone, senderName string) error {
	if n.cfg.TwilioAccountSID == "" || n.cfg.TwilioAuthToken == "" {
		return fmt.Errorf("Twilio credentials not configured")
	}

	quoteURL := fmt.Sprintf("%s/%s", n.cfg.QuoteLinkBaseURL, quote.ShareToken)

	message := fmt.Sprintf(
		"Hi %s! 👋\n\n"+
			"*%s* has sent you a quote:\n\n"+
			"📋 *%s*\n"+
			"💰 Total: *%s %.2f*\n"+
			"📅 Valid until: %s\n\n"+
			"Tap the link below to view and accept:\n%s",
		quote.Client.Name,
		senderName,
		quote.Title,
		quote.Currency,
		quote.Total,
		quote.ExpiresAt.Format("January 2, 2006"),
		quoteURL,
	)

	// Ensure phone has whatsapp: prefix
	to := toPhone
	if !strings.HasPrefix(to, "whatsapp:") {
		to = "whatsapp:" + to
	}

	return n.twilioSendWhatsApp(to, message)
}

func (n *NotificationService) twilioSendWhatsApp(to, message string) error {
	apiURL := fmt.Sprintf(
		"https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json",
		n.cfg.TwilioAccountSID,
	)

	data := url.Values{}
	data.Set("From", n.cfg.TwilioWhatsAppNumber)
	data.Set("To", to)
	data.Set("Body", message)
	payload := strings.NewReader(data.Encode())

	req, err := http.NewRequest("POST", apiURL, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(n.cfg.TwilioAccountSID, n.cfg.TwilioAuthToken)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("twilio request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("twilio returned %d", resp.StatusCode)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// EMAIL TEMPLATE
// ─────────────────────────────────────────────────────────────────────────────

type QuoteEmailData struct {
	ClientName  string
	SenderName  string
	QuoteTitle  string
	QuoteNumber string
	Total       string
	QuoteURL    string
	ExpiresAt   string
}

func quoteEmailHTML(d QuoteEmailData) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"/></head>
<body style="margin:0;padding:0;background:#F5F2EC;font-family:'DM Sans',Arial,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td align="center" style="padding:40px 20px;">
<table width="560" style="background:#fff;border-radius:16px;overflow:hidden;border:1px solid #D8D3C8;">
  <!-- Header -->
  <tr>
    <td style="background:#0D0D0D;padding:28px 36px;">
      <span style="font-size:22px;font-weight:800;color:#fff;letter-spacing:-0.5px;">
        Quote<span style="color:#E85C2F;">Flow</span>
      </span>
    </td>
  </tr>
  <!-- Body -->
  <tr>
    <td style="padding:36px;">
      <p style="margin:0 0 8px;font-size:22px;font-weight:700;color:#0D0D0D;">
        Hi %s,
      </p>
      <p style="margin:0 0 24px;font-size:15px;color:#8A8278;line-height:1.6;">
        <strong style="color:#0D0D0D;">%s</strong> has sent you a quote for review.
      </p>
      <!-- Quote Card -->
      <div style="background:#EDE9DF;border-radius:12px;padding:24px;margin-bottom:28px;">
        <p style="margin:0 0 4px;font-size:11px;letter-spacing:1.2px;text-transform:uppercase;color:#8A8278;font-weight:600;">%s</p>
        <p style="margin:0 0 16px;font-size:20px;font-weight:700;color:#0D0D0D;">%s</p>
        <table width="100%%">
          <tr>
            <td>
              <span style="font-size:12px;color:#8A8278;">Total Amount</span><br/>
              <span style="font-size:26px;font-weight:800;color:#E85C2F;font-family:monospace;">%s</span>
            </td>
            <td style="text-align:right;">
              <span style="font-size:12px;color:#8A8278;">Valid Until</span><br/>
              <span style="font-size:15px;font-weight:600;color:#0D0D0D;">%s</span>
            </td>
          </tr>
        </table>
      </div>
      <!-- CTA Button -->
      <div style="text-align:center;margin-bottom:28px;">
        <a href="%s"
           style="display:inline-block;background:#2DAB6F;color:#fff;padding:15px 40px;
                  border-radius:10px;text-decoration:none;font-size:16px;font-weight:600;">
          View &amp; Accept Quote →
        </a>
      </div>
      <p style="margin:0;font-size:13px;color:#8A8278;text-align:center;line-height:1.6;">
        Or copy this link: <a href="%s" style="color:#E85C2F;">%s</a>
      </p>
    </td>
  </tr>
  <!-- Footer -->
  <tr>
    <td style="background:#EDE9DF;padding:20px 36px;text-align:center;">
      <p style="margin:0;font-size:12px;color:#8A8278;">
        Powered by <strong>QuoteFlow</strong> · Professional quotes for Caribbean freelancers
      </p>
    </td>
  </tr>
</table>
</td></tr>
</table>
</body>
</html>`,
		d.ClientName, d.SenderName,
		d.QuoteNumber, d.QuoteTitle,
		d.Total, d.ExpiresAt,
		d.QuoteURL, d.QuoteURL, d.QuoteURL,
	)
}
