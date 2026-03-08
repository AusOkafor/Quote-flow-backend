package digest

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"quoteflow-backend/config"
	"quoteflow-backend/internal/models"
	"quoteflow-backend/internal/repository"
	"quoteflow-backend/internal/services"
)

// Service sends weekly digest emails.
type Service struct {
	db    *repository.DB
	email *services.EmailService
	cfg   *config.Config
}

// NewService creates a new digest service.
func NewService(db *repository.DB, email *services.EmailService, cfg *config.Config) *Service {
	return &Service{db: db, email: email, cfg: cfg}
}

var digestTips = []string{
	"Follow up with clients 48 hours before their quote expires — a quick WhatsApp message can double your acceptance rate.",
	"Add a short personal note to each quote. Clients are more likely to accept quotes that feel personal.",
	"Require a deposit upfront. It protects your work and filters out clients who aren't serious.",
	"Send quotes within 2 hours of an enquiry. The faster you respond, the higher your win rate.",
	"Keep your quote terms clear and specific — vague terms lead to scope creep and disputes.",
	"Use your business logo on every quote. Branded quotes get accepted 30% more often.",
	"PDF your accepted quotes and keep them for your records. They're your legal protection.",
}

// SendWeeklyDigests sends weekly digest emails to all users with notify_weekly = true.
func (s *Service) SendWeeklyDigests(ctx context.Context) error {
	log.Printf("[Digest] starting weekly digest")

	users, err := s.db.GetUsersForWeeklyDigest(ctx)
	if err != nil {
		log.Printf("[Digest] failed to get users: %v", err)
		return err
	}

	log.Printf("[Digest] found %d users with notify_weekly=true", len(users))

	weekStart := getLastMonday().AddDate(0, 0, -7)
	weekEnd := getLastMonday().AddDate(0, 0, -1)

	tipIdx := int(weekStart.Unix()/604800) % len(digestTips)
	if tipIdx < 0 {
		tipIdx = -tipIdx
	}
	tip := digestTips[tipIdx]

	dashboardURL := strings.TrimSuffix(s.cfg.FrontendURL, "/") + "/app"

	for _, user := range users {
		stats, err := s.db.GetWeeklyDigestStats(ctx, user.ID, weekStart, weekEnd)
		if err != nil {
			log.Printf("[Digest] failed to get stats for user %s: %v", user.ID, err)
			continue
		}

		data := services.WeeklyDigestData{
			FreelancerName:   user.FirstName,
			BusinessName:     user.BusinessName,
			WeekStart:        weekStart.Format("January 2"),
			WeekEnd:          weekEnd.Format("January 2, 2006"),
			QuotesSent:       stats.QuotesSent,
			QuotesAccepted:   stats.QuotesAccepted,
			QuotesViewed:     stats.QuotesViewed,
			QuotesExpiring:   len(stats.ExpiringQuotes),
			PaymentsReceived: stats.PaymentsReceived,
			TotalEarned:      repository.FormatAmountForCurrency(stats.TotalEarned, user.Currency),
			ExpiringQuotes:   toEmailDigestQuotes(stats.ExpiringQuotes),
			AcceptedQuotes:   toEmailDigestQuotes(stats.AcceptedQuotes),
			Tip:              tip,
			DashboardURL:     dashboardURL,
		}

		if err := s.email.SendWeeklyDigest(user.Email, data); err != nil {
			log.Printf("[Digest] failed to send to %s: %v", user.Email, err)
			continue
		}

		log.Printf("[Digest] sent to %s", user.Email)
	}

	log.Printf("[Digest] weekly digest complete")
	return nil
}

func toEmailDigestQuotes(qs []models.DigestQuote) []services.DigestQuote {
	out := make([]services.DigestQuote, len(qs))
	for i, q := range qs {
		out[i] = services.DigestQuote{
			QuoteNumber: q.QuoteNumber,
			ClientName:  q.ClientName,
			Amount:      q.Amount,
			ExpiryDate:  q.ExpiryDate,
			URL:         q.URL,
		}
	}
	return out
}

func getLastMonday() time.Time {
	loc := jamaicaTime()
	now := time.Now().In(loc)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return now.AddDate(0, 0, -(weekday - 1)).Truncate(24 * time.Hour)
}

func jamaicaTime() *time.Location {
	loc, err := time.LoadLocation("America/Jamaica")
	if err != nil {
		return time.UTC
	}
	return loc
}
