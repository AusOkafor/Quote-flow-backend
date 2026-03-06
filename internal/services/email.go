package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"time"

	"quoteflow-backend/config"
)

// EmailService handles transactional emails (e.g. payment receipts).
type EmailService struct {
	cfg *config.Config
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{cfg: cfg}
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
	subject := fmt.Sprintf("Payment Receipt - %s", data.QuoteNumber)

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

func paymentReceiptHTML(data PaymentReceiptData, paidTo string) string {
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
    </td>
  </tr>
  <tr>
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
          View Quote →
        </a>
      </div>
      <p style="margin:0;font-size:14px;color:#555;">Thank you for your payment.</p>
      <hr style="border:none;border-top:1px solid #E5E2DB;margin:24px 0 0;"/>
      <p style="margin:16px 0 0;font-size:12px;color:#8A8278;text-align:center;">
        QuoteFlow · Powered by QuoteFlow
      </p>
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
		html.EscapeString(data.QuoteNumber),
		html.EscapeString(paidTo),
		data.QuoteURL,
	)
}
