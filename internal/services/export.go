package services

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"time"

	"quoteflow-backend/internal/models"
)

// ExportQuotesCSV converts a slice of quotes into a CSV byte buffer.
// This is what gets streamed back to the browser on GET /quotes/export.
func ExportQuotesCSV(quotes []models.Quote) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	// Header row
	if err := w.Write([]string{
		"Quote Number",
		"Client",
		"Service / Title",
		"Status",
		"Currency",
		"Subtotal",
		"Tax Amount",
		"Total",
		"Created Date",
		"Expiry Date",
		"Sent At",
		"Accepted At",
		"View Count",
	}); err != nil {
		return nil, err
	}

	// Data rows
	for _, q := range quotes {
		clientName := ""
		if q.Client != nil {
			clientName = q.Client.Name
			if q.Client.Company != "" {
				clientName = q.Client.Name + " (" + q.Client.Company + ")"
			}
		}

		row := []string{
			q.QuoteNumber,
			clientName,
			q.Title,
			string(q.Status),
			q.Currency,
			fmt.Sprintf("%.2f", q.Subtotal),
			fmt.Sprintf("%.2f", q.TaxAmount),
			fmt.Sprintf("%.2f", q.Total),
			q.CreatedAt.Format("2006-01-02"),
			q.ExpiresAt.Format("2006-01-02"),
			formatTimePtr(q.SentAt),
			formatTimePtr(q.AcceptedAt),
			fmt.Sprintf("%d", q.ViewCount),
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("csv flush: %w", err)
	}

	return buf.Bytes(), nil
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}
