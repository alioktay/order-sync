package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"order-sync/internal/config"
	"order-sync/internal/db"
	gosync "order-sync/internal/sync"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func NewHTTPServer(router *gin.Engine, cfg config.Config) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
func RegisterLifecycle(lc fx.Lifecycle, server *http.Server, migrator db.Migrator, pool *pgxpool.Pool, worker *gosync.Worker, logger *slog.Logger) {
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := migrator.Migrate(ctx); err != nil {
			if pool != nil {
				pool.Close()
			}
			return fmt.Errorf("run database migrations: %w", err)
		}
		if err := startHTTPServer(ctx, server, logger); err != nil {
			if pool != nil {
				pool.Close()
			}
			return fmt.Errorf("start HTTP server: %w", err)
		}
		if worker != nil {
			worker.Start()
		}
		return nil
	}, OnStop: func(ctx context.Context) error {
		var stopErrors []error
		if worker != nil {
			if err := worker.Stop(ctx); err != nil {
				stopErrors = append(stopErrors, fmt.Errorf("stop SAP worker: %w", err))
			}
		}
		if server != nil {
			if err := server.Shutdown(ctx); err != nil {
				stopErrors = append(stopErrors, fmt.Errorf("shutdown HTTP server: %w", err))
			}
		}
		if pool != nil {
			pool.Close()
		}
		return errors.Join(stopErrors...)
	}})
}

func startHTTPServer(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if logger != nil {
				logError(logger, ctx, "0001", "HTTP server failed", err)
			}
		}
	}()
	return nil
}
