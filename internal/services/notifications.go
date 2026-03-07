package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/copy"
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
func (n *NotificationService) SendQuoteByEmail(quote *models.QuoteWithDetails, recipientEmail, senderName string, whiteLabel bool) error {
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
	}, whiteLabel)

	to := recipientEmail
	if to == "" {
		to = quote.Client.Email
	}

	subject := copy.QuoteSubject(quote.QuoteNumber)
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

// SendPaymentReceivedNotification notifies the freelancer that a payment was received.
func (n *NotificationService) SendPaymentReceivedNotification(quote *models.QuoteWithDetails, payment *models.Payment, freelancerEmail string) error {
	if freelancerEmail == "" {
		return nil
	}
	typeLabel := map[string]string{
		"full":    "Full Payment",
		"deposit": "Deposit",
		"balance": "Final Balance",
	}[string(payment.PaymentType)]
	if typeLabel == "" {
		typeLabel = "Payment"
	}
	processorLabel := map[string]string{
		"stripe": "Stripe",
		"paypal": "PayPal",
		"wipay":  "WiPay",
	}[string(payment.Processor)]
	if processorLabel == "" {
		processorLabel = string(payment.Processor)
	}

	appURL := n.cfg.AppURL
	if appURL == "" {
		appURL = "https://quoteflow.app"
	}

	html := fmt.Sprintf(`
<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
  <h2 style="color:#2DAB6F;margin-bottom:8px;">💰 Payment Received!</h2>
  <p style="color:#555;font-size:15px;line-height:1.6;margin-bottom:20px;">
    <strong>%s</strong> has paid the <strong>%s</strong>
    for quote <strong>%s — %s</strong> via <strong>%s</strong>.
  </p>
  <div style="background:#f0fdf4;border:1px solid #bbf7d0;border-radius:8px;padding:20px;margin-bottom:24px;">
    <table style="width:100%%;font-size:14px;color:#374151;">
      <tr><td>Amount paid</td>    <td align="right"><strong>%s %.2f</strong></td></tr>
      <tr><td>QuoteFlow fee (0.7%%)</td><td align="right" style="color:#9ca3af;">-%s %.2f</td></tr>
      <tr style="border-top:1px solid #d1fae5;">
        <td><strong>Net to you</strong></td>
        <td align="right"><strong style="color:#16a34a;">%s %.2f</strong></td>
      </tr>
    </table>
  </div>
  <a href="%s/app" style="display:inline-block;background:#2DAB6F;color:#fff;padding:13px 28px;
     border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;">
    View in QuoteFlow →
  </a>
</div>`,
		quote.Client.Name, typeLabel, quote.QuoteNumber, quote.Title, processorLabel,
		payment.Currency, payment.Amount,
		payment.Currency, payment.PlatformFee,
		payment.Currency, payment.NetAmount,
		appURL,
	)

	subject := fmt.Sprintf("💰 %s paid %s %.2f — %s",
		quote.Client.Name, payment.Currency, payment.Amount, quote.QuoteNumber)
	return n.sendEmail(freelancerEmail, subject, html)
}

// SendChangeRequestNotification notifies the freelancer that the client requested changes.
func (n *NotificationService) SendChangeRequestNotification(quote *models.Quote, clientName, requesterName, requestMessage, freelancerEmail string) error {
	displayName := requesterName
	if displayName == "" {
		displayName = clientName
	}
	if displayName == "" {
		displayName = "The client"
	}
	html := fmt.Sprintf(`
	<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
	  <h2 style="color:#E85C2F;margin-bottom:8px;">✏️ Change Request</h2>
	  <p style="color:#555;font-size:15px;line-height:1.6;">
	    <strong>%s</strong> has requested changes to your quote <strong>%s</strong>.
	  </p>
	  <div style="background:#F5F2EC;border-radius:10px;padding:18px;margin:20px 0;border-left:4px solid #E85C2F;">
	    <p style="margin:0;font-size:14px;color:#333;line-height:1.6;white-space:pre-wrap;">%s</p>
	  </div>
	  <p style="color:#555;font-size:14px;">The quote has been moved back to draft. Make your edits and re-send when ready.</p>
	  <a href="%s/app" style="display:inline-block;margin-top:20px;background:#E85C2F;color:#fff;padding:13px 28px;border-radius:8px;text-decoration:none;font-weight:600;">
	    View &amp; Edit in QuoteFlow →
	  </a>
	</div>`, displayName, quote.QuoteNumber, html.EscapeString(requestMessage), n.cfg.AppURL)

	subject := fmt.Sprintf("✏️ %s requested changes to quote %s", displayName, quote.QuoteNumber)
	return n.sendEmail(freelancerEmail, subject, html)
}

// SendExpiryReminderToClient sends a reminder to the client that their quote expires in 3 days.
func (n *NotificationService) SendExpiryReminderToClient(quote *models.QuoteWithDetails, senderName string) error {
	to := quote.Client.Email
	if to == "" {
		return fmt.Errorf("client has no email")
	}
	quoteURL := fmt.Sprintf("%s/%s", n.cfg.QuoteLinkBaseURL, quote.ShareToken)
	expiryStr := quote.ExpiresAt.Format("January 2, 2006")
	html := fmt.Sprintf(`
	<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
	  <h2 style="color:#C9A84C;margin-bottom:8px;">⏰ Quote Expiring Soon</h2>
	  <p style="color:#555;font-size:15px;line-height:1.6;">
	    Hi %s, your quote <strong>%s</strong> from <strong>%s</strong> expires on <strong>%s</strong>.
	  </p>
	  <p style="color:#555;font-size:14px;">Total: <strong>%s %.2f</strong></p>
	  <a href="%s" style="display:inline-block;margin-top:20px;background:#2DAB6F;color:#fff;padding:13px 28px;border-radius:8px;text-decoration:none;font-weight:600;">
	    View &amp; Accept Quote →
	  </a>
	</div>`, quote.Client.Name, quote.QuoteNumber, senderName, expiryStr, quote.Currency, quote.Total, quoteURL)
	subject := fmt.Sprintf("⏰ Quote %s expires soon — %s", quote.QuoteNumber, quote.Title)
	return n.sendEmail(to, subject, html)
}

// SendExpiringSoonToFreelancer notifies the freelancer that a quote is expiring in 3 days.
func (n *NotificationService) SendExpiringSoonToFreelancer(quote *models.QuoteWithDetails, freelancerEmail string) error {
	html := fmt.Sprintf(`
	<div style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:32px;">
	  <h2 style="color:#C9A84C;margin-bottom:8px;">⏰ Quote Expiring Soon</h2>
	  <p style="color:#555;font-size:15px;line-height:1.6;">
	    Your quote <strong>%s</strong> for <strong>%s</strong> expires in 3 days.
	  </p>
	  <p style="color:#555;font-size:14px;">Total: <strong>%s %.2f</strong></p>
	  <a href="%s/app" style="display:inline-block;margin-top:20px;background:#E85C2F;color:#fff;padding:13px 28px;border-radius:8px;text-decoration:none;font-weight:600;">
	    View in QuoteFlow →
	  </a>
	</div>`, quote.QuoteNumber, quote.Client.Name, quote.Currency, quote.Total, n.cfg.AppURL)
	subject := fmt.Sprintf("⏰ Quote %s expires in 3 days", quote.QuoteNumber)
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

func quoteEmailHTML(d QuoteEmailData, whiteLabel bool) string {
	headerRow := ""
	footer := ""
	if !whiteLabel {
		headerRow = `<tr>
    <td style="background:#0D0D0D;padding:28px 36px;">
      <span style="font-size:22px;font-weight:800;color:#fff;letter-spacing:-0.5px;">
        Quote<span style="color:#E85C2F;">Flow</span>
      </span>
    </td>
  </tr>
  `
		footer = `<tr>
    <td style="background:#EDE9DF;padding:20px 36px;text-align:center;">
      <p style="margin:0;font-size:12px;color:#8A8278;">
        Powered by <strong>QuoteFlow</strong> · Professional quotes for Caribbean freelancers
      </p>
    </td>
  </tr>`
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"/></head>
<body style="margin:0;padding:0;background:#F5F2EC;font-family:'DM Sans',Arial,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td align="center" style="padding:40px 20px;">
<table width="560" style="background:#fff;border-radius:16px;overflow:hidden;border:1px solid #D8D3C8;">
  %s<!-- Body -->
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
  %s
</table>
</td></tr>
</table>
</body>
</html>`,
		headerRow,
		d.ClientName, d.SenderName,
		d.QuoteNumber, d.QuoteTitle,
		d.Total, d.ExpiresAt,
		d.QuoteURL, d.QuoteURL, d.QuoteURL,
		footer,
	)
}
