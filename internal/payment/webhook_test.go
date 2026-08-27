package payment

import "testing"

func TestNormalizePaymentWebhook(t *testing.T) {
	webhook := NormalizePaymentWebhook(Webhook{EventID: " event ", ReferenceOrderID: " order ", PaymentStatus: " completed ", Timestamp: " 2026-08-26T10:00:00Z "})
	if webhook.EventID != "event" || webhook.ReferenceOrderID != "order" || webhook.PaymentStatus != "COMPLETED" || webhook.Timestamp != "2026-08-26T10:00:00Z" {
		t.Fatalf("unexpected normalized payment webhook: %+v", webhook)
	}
}

func TestValidatePaymentWebhook(t *testing.T) {
	valid := Webhook{EventID: "event", ReferenceOrderID: "order", PaymentStatus: "COMPLETED", Timestamp: "2026-08-26T10:00:00Z"}
	if issues := ValidatePaymentWebhook(valid); len(issues) != 0 {
		t.Fatalf("valid payment webhook has issues: %+v", issues)
	}
	if issues := ValidatePaymentWebhook(Webhook{}); len(issues) != 4 {
		t.Fatalf("invalid payment webhook has %d issues, want 4", len(issues))
	}
}
