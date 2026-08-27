package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"order-sync/internal/config"
	"order-sync/internal/db"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

type lifecycleRecorder struct{ hooks []fx.Hook }

func (r *lifecycleRecorder) Append(hook fx.Hook) { r.hooks = append(r.hooks, hook) }

type migratorStub struct{ err error }

func (m migratorStub) Migrate(context.Context) error { return m.err }

var _ db.Migrator = migratorStub{}

func TestStartHTTPServerReportsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	server := &http.Server{Addr: listener.Addr().String()}
	if err = startHTTPServer(context.Background(), server, testLogger()); err == nil {
		t.Fatal("expected an occupied address to fail during startup")
	}
}

func TestHTTPServerConstructionAndStart(t *testing.T) {
	router := gin.New()
	server := NewHTTPServer(router, config.Config{Port: 4321})
	if server.Addr != ":4321" || server.Handler != router || server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("unexpected server: addr=%q handler=%T", server.Addr, server.Handler)
	}

	server = &http.Server{Addr: "127.0.0.1:0", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	if err := startHTTPServer(context.Background(), server, testLogger()); err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIntegerFormatting(t *testing.T) {
	for _, test := range []struct {
		value int
		want  string
	}{{0, "0"}, {7, "7"}, {42, "42"}, {3000, "3000"}} {
		if got := strconv.Itoa(test.value); got != test.want {
			t.Fatalf("itoa(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestRegisterLifecyclePropagatesMigrationFailure(t *testing.T) {
	lifecycle := &lifecycleRecorder{}
	expected := errors.New("migration failed")
	RegisterLifecycle(lifecycle, &http.Server{}, migratorStub{err: expected}, nil, nil, testLogger())
	if len(lifecycle.hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(lifecycle.hooks))
	}
	if err := lifecycle.hooks[0].OnStart(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("OnStart() = %v", err)
	}
}
