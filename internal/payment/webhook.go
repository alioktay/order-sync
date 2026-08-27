package payment

import (
	"strings"
	"time"

	"order-sync/internal/contracts"
)

type Webhook struct {
	EventID          string                  `json:"event_id"`
	ReferenceOrderID string                  `json:"reference_order_id"`
	PaymentStatus    contracts.PaymentStatus `json:"payment_status"`
	Timestamp        string                  `json:"timestamp"`
}

func NormalizePaymentWebhook(p Webhook) Webhook {
	p.EventID = strings.TrimSpace(p.EventID)
	p.ReferenceOrderID = strings.TrimSpace(p.ReferenceOrderID)
	p.PaymentStatus = contracts.PaymentStatus(strings.ToUpper(strings.TrimSpace(string(p.PaymentStatus))))
	p.Timestamp = strings.TrimSpace(p.Timestamp)
	return p
}

func ValidatePaymentWebhook(p Webhook) []contracts.ValidationIssue {
	issues := make([]contracts.ValidationIssue, 0)
	if p.EventID == "" {
		issues = append(issues, issue("event_id", "Required"))
	}
	if p.ReferenceOrderID == "" {
		issues = append(issues, issue("reference_order_id", "Required"))
	}
	if p.PaymentStatus == "" {
		issues = append(issues, issue("payment_status", "Required"))
	}
	if p.PaymentStatus != "" && p.PaymentStatus != contracts.PaymentStatusPending && p.PaymentStatus != contracts.PaymentStatusCompleted && p.PaymentStatus != contracts.PaymentStatusFailed && p.PaymentStatus != contracts.PaymentStatusCancelled {
		issues = append(issues, issue("payment_status", "Unsupported status"))
	}
	if _, err := time.Parse(time.RFC3339, p.Timestamp); err != nil {
		issues = append(issues, issue("timestamp", "Invalid ISO datetime"))
	}
	return issues
}

func issue(field, message string) contracts.ValidationIssue {
	return contracts.ValidationIssue{Code: "invalid_value", Path: []any{field}, Message: message}
}
