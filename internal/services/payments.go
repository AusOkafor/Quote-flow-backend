package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentlink"
	"github.com/stripe/stripe-go/v76/price"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
)

// PaymentService handles payment processor integrations (Stripe Connect, PayPal).
type PaymentService struct {
	cfg    *config.Config
	client *http.Client
}

// NewPaymentService creates a new PaymentService.
func NewPaymentService(cfg *config.Config) *PaymentService {
	return &PaymentService{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// payPalBaseURL returns the PayPal API base URL (sandbox or live).
func (p *PaymentService) payPalBaseURL() string {
	if strings.ToLower(p.cfg.PayPalEnvironment) == "live" {
		return "https://api.paypal.com"
	}
	return "https://api.sandbox.paypal.com"
}

// ── STRIPE ────────────────────────────────────────────────────────────────────

// StripePaymentLink holds the ID and URL of a created Stripe Payment Link.
type StripePaymentLink struct {
	ID  string
	URL string
}

// StripeOAuthResponse is the response from exchanging a Stripe OAuth code.
type StripeOAuthResponse struct {
	StripeUserID string `json:"stripe_user_id"`
	AccessToken  string `json:"access_token"`
}

// ExchangeStripeCode exchanges an OAuth authorization code for access token (Stripe Connect).
func (p *PaymentService) ExchangeStripeCode(code string) (*StripeOAuthResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)

	req, err := http.NewRequest("POST", "https://connect.stripe.com/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.cfg.StripeSecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.ErrorDescription != "" {
			return nil, fmt.Errorf("%s: %s", errBody.Error, errBody.ErrorDescription)
		}
		return nil, fmt.Errorf("stripe oauth exchange failed: %s", resp.Status)
	}

	var result StripeOAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateStripePaymentLink creates a Payment Link on the connected account with application_fee_amount.
func (p *PaymentService) CreateStripePaymentLink(
	account *models.PaymentAccountFull,
	quote *models.QuoteWithDetails,
	amount, platformFee float64,
	paymentType models.PaymentType,
) (*StripePaymentLink, error) {
	amountCents := int64(amount * 100)
	feeCents := int64(platformFee * 100)

	label := map[string]string{
		"full":    "Full Payment",
		"deposit": "Deposit",
		"balance": "Final Balance",
	}[string(paymentType)]
	if label == "" {
		label = "Payment"
	}

	description := fmt.Sprintf("%s — %s · %s", label, quote.Title, quote.QuoteNumber)
	currency := strings.ToLower(quote.Currency)

	stripe.Key = p.cfg.StripeSecretKey

	// Step 1: Create a Price on the connected account
	priceParams := &stripe.PriceParams{
		Currency:   stripe.String(currency),
		UnitAmount: stripe.Int64(amountCents),
		ProductData: &stripe.PriceProductDataParams{
			Name: stripe.String(description),
		},
	}
	priceParams.SetStripeAccount(account.StripeAccountID)

	pr, err := price.New(priceParams)
	if err != nil {
		return nil, fmt.Errorf("create price: %w", err)
	}

	// Step 2: Create Payment Link on the connected account
	redirectURL := strings.TrimSuffix(p.cfg.FrontendURL, "/") + "/payment/complete?quote=" + url.QueryEscape(quote.ShareToken)

	linkParams := &stripe.PaymentLinkParams{
		LineItems: []*stripe.PaymentLinkLineItemParams{
			{
				Price:    stripe.String(pr.ID),
				Quantity: stripe.Int64(1),
			},
		},
		ApplicationFeeAmount: stripe.Int64(feeCents),
		AfterCompletion: &stripe.PaymentLinkAfterCompletionParams{
			Type: stripe.String(string(stripe.PaymentLinkAfterCompletionTypeRedirect)),
			Redirect: &stripe.PaymentLinkAfterCompletionRedirectParams{
				URL: stripe.String(redirectURL),
			},
		},
		Metadata: map[string]string{
			"quote_id":     quote.ID,
			"payment_type": string(paymentType),
		},
	}
	linkParams.SetStripeAccount(account.StripeAccountID)

	pl, err := paymentlink.New(linkParams)
	if err != nil {
		return nil, fmt.Errorf("create payment link: %w", err)
	}

	return &StripePaymentLink{ID: pl.ID, URL: pl.URL}, nil
}

// ── PAYPAL ────────────────────────────────────────────────────────────────────

// PayPalOrder holds the order ID and approval URL for a PayPal order.
type PayPalOrder struct {
	OrderID    string
	ApproveURL string
}

// CreatePayPalOnboardingLink creates a PayPal Commerce Platform onboarding link.
func (p *PaymentService) CreatePayPalOnboardingLink(userID string) (string, error) {
	if p.cfg.PayPalClientID == "" || p.cfg.PayPalClientSecret == "" {
		log.Printf("[PayPal] CreateOnboardingLink failed: PayPal not configured (missing CLIENT_ID or CLIENT_SECRET)")
		return "", fmt.Errorf("PayPal not configured")
	}
	token, err := p.getPayPalPlatformToken()
	if err != nil {
		log.Printf("[PayPal] CreateOnboardingLink failed at token: %v", err)
		return "", err
	}

	body := map[string]interface{}{
		"tracking_id": userID,
		"operations": []map[string]interface{}{
			{
				"operation": "API_INTEGRATION",
				"api_integration_preference": map[string]interface{}{
					"rest_api_integration": map[string]interface{}{
						"integration_method": "PAYPAL",
						"integration_type":   "THIRD_PARTY",
						"third_party_details": map[string]interface{}{
							"features": []string{"PAYMENT", "REFUND"},
						},
					},
				},
			},
		},
		"products":       []string{"EXPRESS_CHECKOUT"},
		"legal_consents": []map[string]interface{}{{"type": "SHARE_DATA_CONSENT", "granted": true}},
		"partner_config_override": map[string]interface{}{
			"return_url": strings.TrimSuffix(p.cfg.AppURL, "/") + "/payments/connect/paypal/callback",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.payPalBaseURL()+"/v2/customer/partner-referrals", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[PayPal] CreateOnboardingLink HTTP request failed: %v", err)
		return "", fmt.Errorf("PayPal request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Check for PayPal error response
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			Details []struct {
				Field   string `json:"field"`
				Issue   string `json:"issue"`
				Details string `json:"details"`
			} `json:"details"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Message
		if msg == "" {
			msg = string(respBody)
		}
		if len(errResp.Details) > 0 && errResp.Details[0].Issue != "" {
			msg = fmt.Sprintf("%s — %s", msg, errResp.Details[0].Issue)
		}
		log.Printf("[PayPal] CreateOnboardingLink partner-referrals failed: HTTP %d | raw: %s", resp.StatusCode, string(respBody))
		return "", fmt.Errorf("PayPal partner-referrals (HTTP %d): %s", resp.StatusCode, msg)
	}

	var result struct {
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("PayPal response parse error: %w", err)
	}
	for _, link := range result.Links {
		if link.Rel == "action_url" {
			return link.Href, nil
		}
	}
	log.Printf("[PayPal] CreateOnboardingLink no action_url in response: %s", string(respBody))
	return "", fmt.Errorf("PayPal did not return action_url — check app is enabled for Commerce Platform")
}

// CreatePayPalOrder creates a PayPal order for USD payments.
func (p *PaymentService) CreatePayPalOrder(
	account *models.PaymentAccountFull,
	quote *models.QuoteWithDetails,
	amount, platformFee float64,
	paymentType models.PaymentType,
) (*PayPalOrder, error) {
	if quote.Currency != "USD" {
		return nil, fmt.Errorf("PayPal only supports USD. Use Stripe for %s", quote.Currency)
	}
	token, err := p.getPayPalPlatformToken()
	if err != nil {
		return nil, err
	}

	label := map[string]string{"full": "Full Payment", "deposit": "Deposit", "balance": "Final Balance"}[string(paymentType)]
	if label == "" {
		label = "Payment"
	}

	body := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{
			{
				"reference_id": quote.ID,
				"description":  fmt.Sprintf("%s — %s · %s", label, quote.Title, quote.QuoteNumber),
				"amount": map[string]interface{}{
					"currency_code": quote.Currency,
					"value":         fmt.Sprintf("%.2f", amount),
				},
				"payment_instruction": map[string]interface{}{
					"disbursement_mode": "INSTANT",
					"platform_fees": []map[string]interface{}{
						{
							"amount": map[string]interface{}{
								"currency_code": quote.Currency,
								"value":         fmt.Sprintf("%.2f", platformFee),
							},
						},
					},
				},
				"payee": map[string]interface{}{
					"merchant_id": account.PayPalMerchantID,
				},
			},
		},
		"application_context": map[string]interface{}{
			"return_url": strings.TrimSuffix(p.cfg.FrontendURL, "/") + "/payment/complete?quote=" + url.QueryEscape(quote.ShareToken),
			"cancel_url": strings.TrimSuffix(p.cfg.FrontendURL, "/") + "/q/" + quote.ShareToken,
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.payPalBaseURL()+"/v2/checkout/orders", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.PayPalBNCode != "" {
		req.Header.Set("PayPal-Partner-Attribution-Id", p.cfg.PayPalBNCode)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		ID    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var approveURL string
	for _, link := range result.Links {
		if link.Rel == "approve" {
			approveURL = link.Href
			break
		}
	}
	if approveURL == "" {
		return nil, fmt.Errorf("no approve URL in PayPal response")
	}
	return &PayPalOrder{OrderID: result.ID, ApproveURL: approveURL}, nil
}

// GetPayPalPlatformToken returns a platform access token for PayPal API calls (used by webhook verification).
func (p *PaymentService) GetPayPalPlatformToken() (string, error) {
	return p.getPayPalPlatformToken()
}

func (p *PaymentService) getPayPalPlatformToken() (string, error) {
	if p.cfg.PayPalClientID == "" || p.cfg.PayPalClientSecret == "" {
		return "", fmt.Errorf("PayPal not configured")
	}
	baseURL := p.payPalBaseURL()
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	req, _ := http.NewRequest("POST", baseURL+"/v1/oauth2/token", strings.NewReader(data.Encode()))
	req.SetBasicAuth(p.cfg.PayPalClientID, p.cfg.PayPalClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[PayPal] getPlatformToken HTTP failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &result)

	if resp.StatusCode != http.StatusOK {
		log.Printf("[PayPal] getPlatformToken failed: HTTP %d | error=%s | error_description=%s | raw: %s",
			resp.StatusCode, result.Error, result.ErrorDesc, string(body))
		if result.ErrorDesc != "" {
			return "", fmt.Errorf("PayPal: %s", result.ErrorDesc)
		}
		if result.Error != "" {
			return "", fmt.Errorf("PayPal: %s", result.Error)
		}
		return "", fmt.Errorf("PayPal auth failed (HTTP %d)", resp.StatusCode)
	}
	if result.AccessToken == "" {
		log.Printf("[PayPal] getPlatformToken no token in 200 response: %s", string(body))
		return "", fmt.Errorf("PayPal: no access token in response")
	}
	return result.AccessToken, nil
}

// ── WIPAY ────────────────────────────────────────────────────────────────────

// wipayEndpoint returns the correct WiPay API URL for the given currency.
// Each Caribbean country has its own dedicated WiPay endpoint.
func wipayEndpoint(currency string) string {
	switch currency {
	case "TTD":
		return "https://tt.wipayfinancial.com/plugins/payments/request"
	case "BBD":
		return "https://bb.wipayfinancial.com/plugins/payments/request"
	case "GYD":
		return "https://gy.wipayfinancial.com/plugins/payments/request"
	default:
		return "https://jm.wipayfinancial.com/plugins/payments/request" // JMD
	}
}

// wipayCountryCode maps currency to WiPay's country_code parameter.
func wipayCountryCode(currency string) string {
	switch currency {
	case "TTD":
		return "TT"
	case "BBD":
		return "BB"
	case "GYD":
		return "GY"
	default:
		return "JM" // JMD
	}
}

// WiPayFormData holds the form fields for a client-side POST to WiPay.
// The browser submits this form directly to WiPay's domain — no CORS issues.
type WiPayFormData struct {
	Endpoint      string
	AccountNumber string
	AVS           string
	CountryCode   string
	Currency      string
	Environment   string
	FeeStructure  string
	Method        string
	OrderID       string
	Origin        string
	ResponseURL   string
	ReturnURL     string
	Total         string
}

// GetWiPayFormData returns form fields for a client-side POST to WiPay.
// The frontend renders an auto-submitting form that POSTs directly to WiPay.
func (p *PaymentService) GetWiPayFormData(
	account *models.PaymentAccountFull,
	amount float64,
	currency string,
	quoteToken string,
) (*WiPayFormData, error) {
	if account.WiPayAccountID == "" || account.WiPayAPIKey == "" {
		return nil, fmt.Errorf("WiPay account not connected")
	}

	env := p.cfg.WiPayEnvironment
	if env == "" {
		env = "sandbox"
	}

	responseURL := strings.TrimSuffix(p.cfg.AppURL, "/") + "/webhooks/wipay"
	// return_url: no query params — & would be treated as form field separator by WiPay
	// Quote page shows paid status from backend; ?payment=success not needed
	returnURL := fmt.Sprintf("%s/q/%s",
		strings.TrimSuffix(p.cfg.FrontendURL, "/"), quoteToken)

	log.Printf("[WiPay] GetWiPayFormData: FrontendURL=%s AppURL=%s ReturnURL=%s",
		p.cfg.FrontendURL, p.cfg.AppURL, returnURL)

	// WiPay rejects order_id with underscores or dashes — sanitize for form
	wipayOrderID := strings.NewReplacer("_", "", "-", "").Replace(quoteToken)

	// WiPay uses origin as base for redirect and appends /app — set to quote URL
	// Frontend /q/:token/app route redirects to /q/:token
	origin := fmt.Sprintf("%s/q/%s",
		strings.TrimSuffix(p.cfg.FrontendURL, "/"), quoteToken)

	return &WiPayFormData{
		Endpoint:      wipayEndpoint(currency),
		AccountNumber: account.WiPayAccountID,
		AVS:           "0",
		CountryCode:   wipayCountryCode(currency),
		Currency:      currency,
		Environment:   env,
		FeeStructure:  "merchant_absorb",
		Method:        "credit_card",
		OrderID:       wipayOrderID,
		Origin:        origin,
		ResponseURL:   responseURL,
		ReturnURL:     returnURL,
		Total:         fmt.Sprintf("%.2f", amount),
	}, nil
}

