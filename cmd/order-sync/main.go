package main

import (
	_ "order-sync/docs"
	"order-sync/internal/app"
	"order-sync/internal/config"
	"order-sync/internal/db"
	"order-sync/internal/infrastructure/postgres/repositories"
	"order-sync/internal/infrastructure/sap"
	"order-sync/internal/orders"
	"order-sync/internal/payment"
	"order-sync/internal/shared"
	"order-sync/internal/shop"
	gosync "order-sync/internal/sync"

	"go.uber.org/fx"
)

// @title example Order Sync API
// @version 1.0
// @description Receives shop and payment webhooks, persists order state, and exposes order status.
// @host localhost:3000
// @BasePath /
// @securityDefinitions.apikey WebhookSecret
// @in header
// @name X-Webhook-Secret
func main() {
	fx.New(
		fx.NopLogger,
		fx.Provide(config.Load, shared.NewLogger, db.NewPoolFromConfig, db.NewMigrator, db.NewTransactionRunner, app.NewHealthCheck,
			repositories.NewPoolOrderRepository, repositories.NewSyncJobRepository, sap.NewClient,
			fx.Annotate(orders.NewService, fx.As(new(orders.API))),
			fx.Annotate(shop.NewService, fx.As(new(shop.API))),
			fx.Annotate(payment.NewService, fx.As(new(payment.API))),
			gosync.NewWorker, app.NewRouter, app.NewHTTPServer),
		fx.Invoke(app.RegisterLifecycle),
	).Run()
}
