package db

import (
	"context"
	"fmt"
	"order-sync/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPoolFromConfig(cfg config.Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	return pgxpool.NewWithConfig(context.Background(), poolConfig)
}
