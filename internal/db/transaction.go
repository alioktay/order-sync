package db

import (
	"context"
	"fmt"
	"order-sync/internal/contracts"
	postgresrepo "order-sync/internal/infrastructure/postgres/repositories"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type PostgresTransactionRunner struct{ pool transactionBeginner }

func NewTransactionRunner(pool *pgxpool.Pool) contracts.TransactionRunner {
	return &PostgresTransactionRunner{pool: pool}
}

func (r *PostgresTransactionRunner) Run(ctx context.Context, work func(contracts.TransactionRepositories) (contracts.WebhookResult, error)) (contracts.WebhookResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repos := contracts.TransactionRepositories{
		Orders:        postgresrepo.NewOrderRepository(tx),
		SKUClassifier: postgresrepo.NewSKUClassifier(tx),
		Events:        postgresrepo.NewWebhookEventRepository(tx),
		Payments:      postgresrepo.NewPaymentRepository(tx),
	}
	result, err := work(repos)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("run transaction work: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("commit transaction: %w", err)
	}
	return result, nil
}
