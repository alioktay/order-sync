package shop

import (
	"encoding/json"
	"testing"

	"order-sync/internal/contracts"
)

func TestOrderItemJSONPreservesOptionalHardwareState(t *testing.T) {
	var items []contracts.OrderItem
	if err := json.Unmarshal([]byte(`[
		{"sku":"omitted","quantity":1,"price":1},
		{"sku":"false","quantity":1,"price":1,"isHardware":false},
		{"sku":"true","quantity":1,"price":1,"isHardware":true}
	]`), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].IsHardware != nil || items[1].IsHardware == nil || *items[1].IsHardware || items[2].IsHardware == nil || !*items[2].IsHardware {
		t.Fatalf("decoded hardware overrides = %#v", items)
	}

	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip []map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if _, ok := roundTrip[0]["isHardware"]; ok {
		t.Fatalf("omitted isHardware was serialized: %s", encoded)
	}
	if got, ok := roundTrip[1]["isHardware"].(bool); !ok || got {
		t.Fatalf("false isHardware serialized as %#v", roundTrip[1]["isHardware"])
	}
	if got, ok := roundTrip[2]["isHardware"].(bool); !ok || !got {
		t.Fatalf("true isHardware serialized as %#v", roundTrip[2]["isHardware"])
	}
}

func TestNormalizeShopWebhook(t *testing.T) {
	webhook := NormalizeShopWebhook(Webhook{
		EventID: " event ", OrderID: " order ", CustomerEmail: " buyer@example.com ",
		Items: []contracts.OrderItem{{SKU: " sku ", Quantity: 1}},
	})
	if webhook.EventID != "event" || webhook.OrderID != "order" || webhook.CustomerEmail != "buyer@example.com" || webhook.Items[0].SKU != "sku" {
		t.Fatalf("unexpected normalized shop webhook: %+v", webhook)
	}
}

func TestValidateShopWebhook(t *testing.T) {
	valid := Webhook{EventID: "event", OrderID: "order", CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "sku", Quantity: 1, Price: 0}}}
	if issues := ValidateShopWebhook(valid); len(issues) != 0 {
		t.Fatalf("valid shop webhook has issues: %+v", issues)
	}

	invalid := Webhook{CustomerEmail: "Buyer <buyer@example.com>", Items: []contracts.OrderItem{{Quantity: 0, Price: -1}}}
	issues := ValidateShopWebhook(invalid)
	if len(issues) != 6 {
		t.Fatalf("invalid shop webhook has %d issues, want 6: %+v", len(issues), issues)
	}
	if issues[3].Code != "invalid_value" || len(issues[3].Path) != 3 {
		t.Fatalf("unexpected item issue: %+v", issues[3])
	}

	invalid.Items = nil
	if issues = ValidateShopWebhook(invalid); len(issues) != 4 {
		t.Fatalf("missing items webhook has %d issues, want 4: %+v", len(issues), issues)
	}
}
