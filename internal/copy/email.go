package copy

import "fmt"

// Email subjects and CTAs — aligned with frontend src/lib/messages.ts

// QuoteSubject returns the subject for quote emails to clients.
func QuoteSubject(quoteNumber string) string {
	return fmt.Sprintf("Your Quote — %s", quoteNumber)
}

// ReceiptSubject returns the subject for payment receipt emails.
func ReceiptSubject(quoteNumber string) string {
	return fmt.Sprintf("Payment Receipt — %s", quoteNumber)
}

// PaymentNotificationSubject returns the subject for payment notification to freelancer.
func PaymentNotificationSubject(clientName string) string {
	return fmt.Sprintf("Payment Received from %s", clientName)
}

// TeamInviteSubject returns the subject for team invite emails.
func TeamInviteSubject(businessName string) string {
	return fmt.Sprintf("You have been invited to join %s on QuoteFlow", businessName)
}

// CTAs
const (
	ViewQuoteCTA     = "View Quote →"
	ViewAcceptCTA    = "View & Accept Quote →"
	ViewInQuoteFlow  = "View in QuoteFlow →"
	ViewEditQuoteFlow = "View & Edit in QuoteFlow →"
)
