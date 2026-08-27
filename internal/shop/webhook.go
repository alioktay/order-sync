package shop

import (
	"net/mail"
	"strings"

	"order-sync/internal/contracts"
)

type Webhook struct {
	EventID       string                `json:"event_id"`
	OrderID       string                `json:"order_id"`
	CustomerEmail string                `json:"customer_email"`
	Items         []contracts.OrderItem `json:"items"`
}

func NormalizeShopWebhook(p Webhook) Webhook {
	p.EventID = strings.TrimSpace(p.EventID)
	p.OrderID = strings.TrimSpace(p.OrderID)
	p.CustomerEmail = strings.TrimSpace(p.CustomerEmail)
	for i := range p.Items {
		p.Items[i].SKU = strings.TrimSpace(p.Items[i].SKU)
	}
	return p
}

func ValidateShopWebhook(p Webhook) []contracts.ValidationIssue {
	issues := make([]contracts.ValidationIssue, 0)
	if p.EventID == "" {
		issues = append(issues, issue("event_id", "Required"))
	}
	if p.OrderID == "" {
		issues = append(issues, issue("order_id", "Required"))
	}
	if p.CustomerEmail == "" {
		issues = append(issues, issue("customer_email", "Required"))
	} else if address, err := mail.ParseAddress(p.CustomerEmail); err != nil || address.Address != p.CustomerEmail {
		issues = append(issues, issue("customer_email", "Invalid email address"))
	}
	if len(p.Items) == 0 {
		issues = append(issues, issue("items", "At least one item is required"))
	}
	for i := range p.Items {
		if p.Items[i].SKU == "" {
			issues = append(issues, issuePath([]any{"items", i, "sku"}, "Required"))
		}
		if p.Items[i].Quantity <= 0 {
			issues = append(issues, issuePath([]any{"items", i, "quantity"}, "Must be a positive integer"))
		}
		if p.Items[i].Price < 0 {
			issues = append(issues, issuePath([]any{"items", i, "price"}, "Must be non-negative"))
		}
	}
	return issues
}

func issue(field, message string) contracts.ValidationIssue {
	return issuePath([]any{field}, message)
}

func issuePath(path []any, message string) contracts.ValidationIssue {
	return contracts.ValidationIssue{Code: "invalid_value", Path: path, Message: message}
}
