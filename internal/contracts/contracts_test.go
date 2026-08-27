package contracts

import "testing"

func TestStatusConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "payment pending", got: string(PaymentStatusPending), want: "PENDING"},
		{name: "payment completed", got: string(PaymentStatusCompleted), want: "COMPLETED"},
		{name: "payment failed", got: string(PaymentStatusFailed), want: "FAILED"},
		{name: "sync pending", got: string(SyncStatusPending), want: "PENDING"},
		{name: "sync processing", got: string(SyncStatusProcessing), want: "PROCESSING"},
		{name: "sync synced", got: string(SyncStatusSynced), want: "SYNCED"},
		{name: "sync waiting", got: string(SyncStatusWaiting), want: "WAITING"},
		{name: "sync dead", got: string(SyncStatusDead), want: "DEAD"},
		{name: "order pending", got: string(OrderStatePending), want: "PENDING"},
		{name: "order paid", got: string(OrderStatePaid), want: "PAID"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("constant = %q, want %q", test.got, test.want)
			}
		})
	}
}
