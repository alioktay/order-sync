package repositories

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSKUClassifier(t *testing.T) {
	t.Run("returns false without querying for an empty order", func(t *testing.T) {
		classifier := NewSKUClassifier(nil)
		hasHardware, err := classifier.HasHardware(context.Background(), nil)
		if err != nil || hasHardware {
			t.Fatalf("HasHardware() = %v, %v; want false, nil", hasHardware, err)
		}
	})

	t.Run("returns database classification", func(t *testing.T) {
		database := fakeDB{queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			if len(args) != 1 {
				t.Fatalf("query args = %#v, want one SKU array", args)
			}
			return fakeRow{scan: func(dest ...any) error {
				*dest[0].(*bool) = true
				return nil
			}}
		}}
		classifier := NewSKUClassifier(database)
		hasHardware, err := classifier.HasHardware(context.Background(), []string{"custom-hardware", "digital"})
		if err != nil || !hasHardware {
			t.Fatalf("HasHardware() = %v, %v; want true, nil", hasHardware, err)
		}
	})

	t.Run("propagates database errors", func(t *testing.T) {
		wantErr := errors.New("classification query failed")
		database := fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: wantErr} }}
		classifier := NewSKUClassifier(database)
		if hasHardware, err := classifier.HasHardware(context.Background(), []string{"sku"}); !errors.Is(err, wantErr) || hasHardware {
			t.Fatalf("HasHardware() = %v, %v; want false, query error", hasHardware, err)
		}
	})
}
