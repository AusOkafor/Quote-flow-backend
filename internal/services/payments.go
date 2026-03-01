package services

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"io"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// PaymentService handles payment processor integrations (Stripe Connect, WiPay, PayPal).
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

// ── WIPAY ────────────────────────────────────────────────────────────────────

// WiPayLink holds the URL and transaction ID of a WiPay payment link.
type WiPayLink struct {
	URL           string
	TransactionID string
}

// ValidateWiPayCredentials optionally validates WiPay API credentials. Returns nil for now.
func (p *PaymentService) ValidateWiPayCredentials(accountID, apiKey string) error {
	_ = accountID
	_ = apiKey
	return nil
}

// CreateWiPayLink creates a WiPay payment link for Caribbean currencies.
func (p *PaymentService) CreateWiPayLink(
	account *models.PaymentAccountFull,
	quote *models.QuoteWithDetails,
	amount float64,
	paymentType models.PaymentType,
) (*WiPayLink, error) {
	country := "JM"
	switch quote.Currency {
	case "TTD":
		country = "TT"
	case "BBD":
		country = "BB"
	}

	data := url.Values{}
	data.Set("account_number", account.WiPayAccountID)
	data.Set("avs", "0")
	data.Set("country_code", country)
	data.Set("currency", quote.Currency)
	data.Set("environment", p.cfg.WiPayEnvironment)
	data.Set("fee_structure", "1")
	data.Set("order_id", quote.ID)
	data.Set("redirect_url", strings.TrimSuffix(p.cfg.FrontendURL, "/")+"/payment/complete?quote="+url.QueryEscape(quote.ShareToken))
	data.Set("total", fmt.Sprintf("%.2f", amount))
	data.Set("hash", p.wipayHash(account.WiPayAPIKey, amount, quote.ID))

	apiURL := strings.TrimSuffix(p.cfg.WiPayAPIURL, "/") + "/transactions/create"
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		URL           string `json:"url"`
		TransactionID string `json:"transaction_id"`
		Status        int    `json:"status"`
		Message       string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("wipay response: %w", err)
	}
	if result.Status != 1 {
		return nil, fmt.Errorf("WiPay error: %s", result.Message)
	}
	return &WiPayLink{URL: result.URL, TransactionID: result.TransactionID}, nil
}

func (p *PaymentService) wipayHash(apiKey string, amount float64, orderID string) string {
	msg := fmt.Sprintf("%s%.2f%s", apiKey, amount, orderID)
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
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
		return "", fmt.Errorf("PayPal not configured")
	}
	token, err := p.getPayPalPlatformToken()
	if err != nil {
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
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	for _, link := range result.Links {
		if link.Rel == "action_url" {
			return link.Href, nil
		}
	}
	return "", fmt.Errorf("no action_url in PayPal response")
}

// CreatePayPalOrder creates a PayPal order for USD payments.
func (p *PaymentService) CreatePayPalOrder(
	account *models.PaymentAccountFull,
	quote *models.QuoteWithDetails,
	amount, platformFee float64,
	paymentType models.PaymentType,
) (*PayPalOrder, error) {
	if quote.Currency != "USD" {
		return nil, fmt.Errorf("PayPal only supports USD. Use WiPay for %s", quote.Currency)
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

func (p *PaymentService) getPayPalPlatformToken() (string, error) {
	if p.cfg.PayPalClientID == "" || p.cfg.PayPalClientSecret == "" {
		return "", fmt.Errorf("PayPal not configured")
	}
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	req, _ := http.NewRequest("POST", p.payPalBaseURL()+"/v1/oauth2/token", strings.NewReader(data.Encode()))
	req.SetBasicAuth(p.cfg.PayPalClientID, p.cfg.PayPalClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
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
		if result.ErrorDesc != "" {
			return "", fmt.Errorf("PayPal: %s", result.ErrorDesc)
		}
		if result.Error != "" {
			return "", fmt.Errorf("PayPal: %s", result.Error)
		}
		return "", fmt.Errorf("PayPal auth failed (HTTP %d)", resp.StatusCode)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("PayPal: no access token in response")
	}
	return result.AccessToken, nil
}
