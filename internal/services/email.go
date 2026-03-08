package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/copy"
)

// EmailService handles transactional emails (e.g. payment receipts).
type EmailService struct {
	cfg *config.Config
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{cfg: cfg}
}

// WeeklyDigestData holds data for the weekly digest email.
type WeeklyDigestData struct {
	FreelancerName   string
	BusinessName     string
	WeekStart        string
	WeekEnd          string
	QuotesSent       int
	QuotesAccepted   int
	QuotesViewed     int
	QuotesExpiring   int
	PaymentsReceived int
	TotalEarned      string
	ExpiringQuotes   []DigestQuote
	AcceptedQuotes   []DigestQuote
	Tip              string
	DashboardURL     string
}

// DigestQuote holds a quote summary for the digest.
type DigestQuote struct {
	QuoteNumber string
	ClientName  string
	Amount      string
	ExpiryDate  string
	URL         string
}

// PaymentReceiptData holds data for the payment receipt email.
type PaymentReceiptData struct {
	// Client
	ClientName  string
	ClientEmail string

	// Freelancer/Business
	FreelancerName string
	BusinessName   string // use if set, else FreelancerName

	// Payment
	ReceiptNumber string // format: QF-RCP-{payment.ID}
	TransactionID string // processor transaction ID
	Processor     string // "WiPay", "Stripe", "PayPal"
	PaymentDate   string // formatted: "March 6, 2026"
	PaymentType   string // "Deposit", "Balance", "Full Payment"

	// Quote
	QuoteNumber string // e.g. QF-012
	QuoteURL    string // full URL to public quote page

	// White-label: true for Business plan — removes QuoteFlow branding
	WhiteLabel bool

	// Amount
	Amount   string // formatted: "J$13,900.00"
	Currency string // JMD, USD, TTD, BBD
}

// SendPaymentReceiptEmail sends a payment receipt to the client.
func (s *EmailService) SendPaymentReceiptEmail(data PaymentReceiptData) error {
	if data.ClientEmail == "" {
		return fmt.Errorf("client email is required")
	}
	if s.cfg.ResendAPIKey == "" {
		return fmt.Errorf("RESEND_API_KEY not configured")
	}

	paidTo := data.BusinessName
	if paidTo == "" {
		paidTo = data.FreelancerName
	}
	if paidTo == "" {
		paidTo = "Your service provider"
	}

	html := paymentReceiptHTML(data, paidTo)
	subject := copy.ReceiptSubject(data.QuoteNumber)

	type resendPayload struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}
	payload := resendPayload{
		From:    fmt.Sprintf("%s <%s>", s.cfg.EmailFromName, s.cfg.EmailFrom),
		To:      []string{data.ClientEmail},
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
	req.Header.Set("Authorization", "Bearer "+s.cfg.ResendAPIKey)
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

// SendWeeklyDigest sends the weekly digest email to a user.
func (s *EmailService) SendWeeklyDigest(to string, data WeeklyDigestData) error {
	if to == "" {
		return fmt.Errorf("recipient email is required")
	}
	if s.cfg.ResendAPIKey == "" {
		return fmt.Errorf("RESEND_API_KEY not configured")
	}

	html := weeklyDigestHTML(data)
	subject := copy.WeeklyDigestSubject(data.WeekEnd)

	type resendPayload struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}
	payload := resendPayload{
		From:    fmt.Sprintf("%s <%s>", s.cfg.EmailFromName, s.cfg.EmailFrom),
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
	req.Header.Set("Authorization", "Bearer "+s.cfg.ResendAPIKey)
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

func weeklyDigestHTML(data WeeklyDigestData) string {
	freelancerName := data.FreelancerName
	if freelancerName == "" {
		freelancerName = "there"
	}
	settingsURL := ""
	if s := data.DashboardURL; s != "" {
		base := strings.TrimSuffix(s, "/")
		settingsURL = base + "/settings?panel=profile"
	}

	// Stats row
	statsRow := fmt.Sprintf(`<tr>
    <td style="width:25%%;padding:12px;text-align:center;background:#EDE9DF;border-radius:10px;margin:4px;">
      <div style="font-size:24px;font-weight:800;color:#0D0D0D;">%d</div>
      <div style="font-size:11px;color:#8A8278;margin-top:4px;">Quotes Sent</div>
    </td>
    <td style="width:25%%;padding:12px;text-align:center;background:#EDE9DF;border-radius:10px;margin:4px;">
      <div style="font-size:24px;font-weight:800;color:#0D0D0D;">%d</div>
      <div style="font-size:11px;color:#8A8278;margin-top:4px;">Accepted</div>
    </td>
    <td style="width:25%%;padding:12px;text-align:center;background:#EDE9DF;border-radius:10px;margin:4px;">
      <div style="font-size:24px;font-weight:800;color:#0D0D0D;">%d</div>
      <div style="font-size:11px;color:#8A8278;margin-top:4px;">Viewed</div>
    </td>
    <td style="width:25%%;padding:12px;text-align:center;background:#EDE9DF;border-radius:10px;margin:4px;">
      <div style="font-size:24px;font-weight:800;color:#0D0D0D;">%d</div>
      <div style="font-size:11px;color:#8A8278;margin-top:4px;">Payments</div>
    </td>
  </tr>`,
		data.QuotesSent, data.QuotesAccepted, data.QuotesViewed, data.PaymentsReceived)

	// Expiring section
	expiringSection := ""
	if data.QuotesExpiring > 0 && len(data.ExpiringQuotes) > 0 {
		rows := ""
		for _, q := range data.ExpiringQuotes {
			rows += fmt.Sprintf(`<tr>
        <td style="padding:12px 0;border-bottom:1px solid #E5E2DB;">
          <strong>%s</strong> — %s<br/>
          <span style="font-size:12px;color:#8A8278;">%s · Expires %s</span><br/>
          <a href="%s" style="font-size:12px;color:#E85C2F;">Follow up →</a>
        </td>
      </tr>`,
				html.EscapeString(q.QuoteNumber), html.EscapeString(q.ClientName),
				html.EscapeString(q.Amount), html.EscapeString(q.ExpiryDate),
				q.URL)
		}
		expiringSection = fmt.Sprintf(`<h3 style="margin:24px 0 12px;font-size:16px;font-weight:700;color:#0D0D0D;">⚠️ Quotes expiring this week</h3>
      <table width="100%%">%s</table>`, rows)
	}

	// Accepted section
	acceptedSection := ""
	if data.QuotesAccepted > 0 && len(data.AcceptedQuotes) > 0 {
		rows := ""
		for _, q := range data.AcceptedQuotes {
			rows += fmt.Sprintf(`<tr>
        <td style="padding:12px 0;border-bottom:1px solid #E5E2DB;">
          <strong>%s</strong> — %s · %s
        </td>
      </tr>`,
				html.EscapeString(q.QuoteNumber), html.EscapeString(q.ClientName), html.EscapeString(q.Amount))
		}
		acceptedSection = fmt.Sprintf(`<h3 style="margin:24px 0 12px;font-size:16px;font-weight:700;color:#0D0D0D;">✅ Accepted this week</h3>
      <table width="100%%">%s</table>`, rows)
	}

	// Tip section
	tipSection := fmt.Sprintf(`<div style="background:#F5F2EC;border-radius:12px;padding:20px;margin:24px 0;">
    <h3 style="margin:0 0 8px;font-size:14px;font-weight:700;color:#0D0D0D;">💡 Tip of the week</h3>
    <p style="margin:0;font-size:14px;color:#555;line-height:1.6;">%s</p>
  </div>`, html.EscapeString(data.Tip))

	// CTA
	ctaSection := ""
	if data.DashboardURL != "" {
		ctaSection = fmt.Sprintf(`<div style="text-align:center;margin:28px 0;">
    <a href="%s" style="display:inline-block;background:#E85C2F;color:#fff;padding:15px 40px;border-radius:10px;text-decoration:none;font-size:16px;font-weight:600;">
      View your dashboard →
    </a>
  </div>`, data.DashboardURL)
	}

	// Footer
	footerUnsub := ""
	if settingsURL != "" {
		footerUnsub = fmt.Sprintf(`<p style="margin:8px 0 0;font-size:12px;color:#8A8278;"><a href="%s" style="color:#E85C2F;">Unsubscribe from weekly digests</a></p>`, settingsURL)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"/></head>
<body style="margin:0;padding:0;background:#F5F2EC;font-family:'DM Sans',Arial,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td align="center" style="padding:40px 20px;">
<table width="560" style="background:#fff;border-radius:16px;overflow:hidden;border:1px solid #D8D3C8;">
  <tr>
    <td style="background:#0D0D0D;padding:28px 36px;">
      <span style="font-size:22px;font-weight:800;color:#fff;letter-spacing:-0.5px;">
        Quote<span style="color:#E85C2F;">Flow</span>
      </span>
      <div style="font-size:12px;color:rgba(255,255,255,.6);margin-top:8px;">Weekly Summary</div>
      <div style="font-size:13px;color:rgba(255,255,255,.5);margin-top:4px;">%s – %s</div>
    </td>
  </tr>
  <tr>
    <td style="padding:36px;">
      <p style="margin:0 0 8px;font-size:22px;font-weight:700;color:#0D0D0D;">Good morning, %s.</p>
      <p style="margin:0 0 24px;font-size:15px;color:#555;line-height:1.6;">Here's your QuoteFlow summary for the week.</p>

      <table width="100%%" cellpadding="0" cellspacing="8" style="margin-bottom:24px;">%s</table>

      <div style="text-align:center;margin:28px 0;padding:24px;background:rgba(232,92,47,.08);border-radius:12px;">
        <div style="font-size:28px;font-weight:800;color:#E85C2F;">%s</div>
        <div style="font-size:14px;color:#8A8278;margin-top:4px;">received this week</div>
      </div>

      %s
      %s
      %s
      %s

      <hr style="border:none;border-top:1px solid #E5E2DB;margin:28px 0 16px;"/>
      <p style="margin:0;font-size:12px;color:#8A8278;">You're receiving this because weekly digests are enabled in your settings.</p>
      %s
      <p style="margin:16px 0 0;font-size:12px;color:#8A8278;text-align:center;">QuoteFlow · Professional quotes for Caribbean freelancers</p>
    </td>
  </tr>
</table>
</td></tr>
</table>
</body>
</html>`,
		html.EscapeString(data.WeekStart), html.EscapeString(data.WeekEnd),
		html.EscapeString(freelancerName),
		statsRow,
		html.EscapeString(data.TotalEarned),
		expiringSection,
		acceptedSection,
		tipSection,
		ctaSection,
		footerUnsub,
	)
}

func paymentReceiptHTML(data PaymentReceiptData, paidTo string) string {
	headerRow := ""
	footerBlock := ""
	if !data.WhiteLabel {
		headerRow = `<tr>
    <td style="background:#0D0D0D;padding:28px 36px;">
      <span style="font-size:22px;font-weight:800;color:#fff;letter-spacing:-0.5px;">
        Quote<span style="color:#E85C2F;">Flow</span>
      </span>
    </td>
  </tr>
  `
		footerBlock = `<hr style="border:none;border-top:1px solid #E5E2DB;margin:24px 0 0;"/>
      <p style="margin:16px 0 0;font-size:12px;color:#8A8278;text-align:center;">
        QuoteFlow · Powered by QuoteFlow
      </p>`
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"/></head>
<body style="margin:0;padding:0;background:#F5F2EC;font-family:'DM Sans',Arial,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td align="center" style="padding:40px 20px;">
<table width="560" style="background:#fff;border-radius:16px;overflow:hidden;border:1px solid #D8D3C8;">
  %s<tr>
    <td style="padding:36px;">
      <h2 style="margin:0 0 8px;font-size:22px;font-weight:700;color:#0D0D0D;">Payment Receipt</h2>
      <hr style="border:none;border-top:1px solid #E5E2DB;margin:0 0 24px;"/>
      <p style="margin:0 0 24px;font-size:15px;color:#555;line-height:1.6;">
        Hi %s,
      </p>
      <p style="margin:0 0 24px;font-size:15px;color:#555;line-height:1.6;">
        Your payment has been received.<br/>
        Here are your payment details:
      </p>
      <div style="background:#EDE9DF;border-radius:12px;padding:24px;margin-bottom:28px;">
        <table width="100%%" style="font-size:14px;color:#374151;">
          <tr><td style="padding:4px 0;color:#8A8278;">Receipt #</td><td style="padding:4px 0;text-align:right;"><strong>%s</strong></td></tr>
          <tr><td style="padding:4px 0;color:#8A8278;">Date</td><td style="padding:4px 0;text-align:right;">%s</td></tr>
          <tr><td style="padding:4px 0;color:#8A8278;">Amount</td><td style="padding:4px 0;text-align:right;"><strong>%s</strong></td></tr>
          <tr><td style="padding:4px 0;color:#8A8278;">Payment Type</td><td style="padding:4px 0;text-align:right;">%s</td></tr>
          <tr><td style="padding:4px 0;color:#8A8278;">Processor</td><td style="padding:4px 0;text-align:right;">%s</td></tr>
          <tr><td style="padding:4px 0;color:#8A8278;">Transaction</td><td style="padding:4px 0;text-align:right;">%s</td></tr>
        </table>
      </div>
      <p style="margin:0 0 8px;font-size:14px;color:#555;">Quote: <strong>%s</strong></p>
      <p style="margin:0 0 24px;font-size:14px;color:#555;">Paid to: <strong>%s</strong></p>
      <div style="text-align:center;margin-bottom:28px;">
        <a href="%s" style="display:inline-block;background:#2DAB6F;color:#fff;padding:15px 40px;border-radius:10px;text-decoration:none;font-size:16px;font-weight:600;">
          %s
        </a>
      </div>
      <p style="margin:0;font-size:14px;color:#555;">Thank you for your payment.</p>
      %s
    </td>
  </tr>
</table>
</td></tr>
</table>
</body>
</html>`,
		html.EscapeString(data.ClientName),
		html.EscapeString(data.ReceiptNumber),
		html.EscapeString(data.PaymentDate),
		html.EscapeString(data.Amount),
		html.EscapeString(data.PaymentType),
		html.EscapeString(data.Processor),
		html.EscapeString(data.TransactionID),
		headerRow,
		html.EscapeString(data.ClientName),
		html.EscapeString(data.ReceiptNumber),
		html.EscapeString(data.PaymentDate),
		html.EscapeString(data.Amount),
		html.EscapeString(data.PaymentType),
		html.EscapeString(data.Processor),
		html.EscapeString(data.TransactionID),
		html.EscapeString(data.QuoteNumber),
		html.EscapeString(paidTo),
		data.QuoteURL,
		copy.ViewQuoteCTA,
		footerBlock,
	)
}
