package orders

import (
	"context"
	"testing"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type fakeOrderRepository struct {
	status *contracts.OrderStatus
	items  map[int64][]contracts.OrderItem
	delay  int
}

type fakeSKUClassifier struct {
	hardware bool
}

func (f fakeSKUClassifier) HasHardware(context.Context, []string) (bool, error) {
	return f.hardware, nil
}

func (f *fakeOrderRepository) Create(context.Context, string, string) (int64, error) { return 1, nil }
func (f *fakeOrderRepository) FindID(context.Context, string) (int64, error)         { return 1, nil }
func (f *fakeOrderRepository) Find(context.Context, string) (*contracts.StoredOrder, error) {
	return nil, nil
}
func (f *fakeOrderRepository) FindStatus(context.Context, string) (*contracts.OrderStatus, error) {
	return f.status, nil
}
func (f *fakeOrderRepository) ListItems(_ context.Context, id int64) ([]contracts.OrderItem, error) {
	return f.items[id], nil
}
func (f *fakeOrderRepository) MarkPaid(context.Context, int64, string) error { return nil }
func (f *fakeOrderRepository) AddItems(_ context.Context, id int64, items []contracts.OrderItem) error {
	if f.items == nil {
		f.items = map[int64][]contracts.OrderItem{}
	}
	f.items[id] = items
	return nil
}
func (f *fakeOrderRepository) ScheduleSync(_ context.Context, _ int64, delay int) error {
	f.delay = delay
	return nil
}
func (f *fakeOrderRepository) Cancel(context.Context, int64) error { return nil }

func TestServiceGetsOrderStatus(t *testing.T) {
	expected := &contracts.OrderStatus{OrderID: "order"}
	status, err := NewService(&fakeOrderRepository{status: expected}).GetOrderStatus(context.Background(), "order")
	if err != nil || status != expected {
		t.Fatalf("GetOrderStatus() = %+v, %v", status, err)
	}
}

func TestMarkPaidAndScheduleUsesHardwareDelay(t *testing.T) {
	repository := &fakeOrderRepository{items: map[int64][]contracts.OrderItem{7: {{SKU: "NUKI-SL3"}}}}
	if err := MarkPaidAndSchedule(context.Background(), repository, fakeSKUClassifier{hardware: true}, config.Config{HardwareSyncDelaySeconds: 30}, 7, repository.items[7], "2026-08-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if repository.delay != 30 {
		t.Fatalf("got delay %d, want 30", repository.delay)
	}
}

func TestMarkPaidAndScheduleUsesConfiguredNonHardwareClassification(t *testing.T) {
	repository := &fakeOrderRepository{items: map[int64][]contracts.OrderItem{7: {{SKU: "NUKI-SL3"}}}}
	if err := MarkPaidAndSchedule(context.Background(), repository, fakeSKUClassifier{}, config.Config{HardwareSyncDelaySeconds: 30}, 7, repository.items[7], "2026-08-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if repository.delay != 0 {
		t.Fatalf("got delay %d, want 0 for non-hardware classification", repository.delay)
	}
}

type recordingSKUClassifier struct {
	hardware bool
	skus     []string
}

func (f *recordingSKUClassifier) HasHardware(_ context.Context, skus []string) (bool, error) {
	f.skus = append([]string(nil), skus...)
	return f.hardware, nil
}

func TestMarkPaidAndScheduleUsesPerItemHardwareOverrides(t *testing.T) {
	trueValue, falseValue := true, false
	tests := []struct {
		name           string
		items          []contracts.OrderItem
		classified     bool
		wantDelay      int
		wantLookupSKUs []string
	}{
		{
			name:           "explicit true overrides database non-hardware",
			items:          []contracts.OrderItem{{SKU: "digital", IsHardware: &trueValue}},
			wantDelay:      30,
			wantLookupSKUs: nil,
		},
		{
			name:           "explicit false overrides database hardware",
			items:          []contracts.OrderItem{{SKU: "hardware", IsHardware: &falseValue}},
			classified:     true,
			wantDelay:      0,
			wantLookupSKUs: nil,
		},
		{
			name:           "omitted item uses database classification",
			items:          []contracts.OrderItem{{SKU: "hardware"}},
			classified:     true,
			wantDelay:      30,
			wantLookupSKUs: []string{"hardware"},
		},
		{
			name:           "unknown omitted SKU remains non-hardware",
			items:          []contracts.OrderItem{{SKU: "unknown"}},
			wantLookupSKUs: []string{"unknown"},
		},
		{
			name: "mixed items combine overrides and fallback",
			items: []contracts.OrderItem{
				{SKU: "forced-digital", IsHardware: &falseValue},
				{SKU: "hardware"},
				{SKU: "forced-hardware", IsHardware: &trueValue},
			},
			classified:     false,
			wantDelay:      30,
			wantLookupSKUs: []string{"hardware"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeOrderRepository{}
			classifier := &recordingSKUClassifier{hardware: test.classified}
			if err := MarkPaidAndSchedule(context.Background(), repository, classifier, config.Config{HardwareSyncDelaySeconds: 30}, 7, test.items, "2026-08-26T10:00:00Z"); err != nil {
				t.Fatal(err)
			}
			if repository.delay != test.wantDelay {
				t.Fatalf("got delay %d, want %d", repository.delay, test.wantDelay)
			}
			if len(classifier.skus) != len(test.wantLookupSKUs) {
				t.Fatalf("lookup SKUs = %#v, want %#v", classifier.skus, test.wantLookupSKUs)
			}
			for i := range test.wantLookupSKUs {
				if classifier.skus[i] != test.wantLookupSKUs[i] {
					t.Fatalf("lookup SKUs = %#v, want %#v", classifier.skus, test.wantLookupSKUs)
				}
			}
		})
	}
}
