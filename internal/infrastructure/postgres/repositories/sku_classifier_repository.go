package repositories

import (
	"context"
	"fmt"

	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/queries"
)

type SKUClassifier struct {
	db DBTX
}

func NewSKUClassifier(db DBTX) contracts.SKUClassifier {
	return &SKUClassifier{db: db}
}

var _ contracts.SKUClassifier = (*SKUClassifier)(nil)

func (r *SKUClassifier) HasHardware(ctx context.Context, skus []string) (bool, error) {
	if len(skus) == 0 {
		return false, nil
	}
	var hasHardware bool
	if err := r.db.QueryRow(ctx, queries.HasHardwareSKU, skus).Scan(&hasHardware); err != nil {
		return false, fmt.Errorf("classify SKUs: %w", err)
	}
	return hasHardware, nil
}
