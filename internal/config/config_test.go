package config

import (
	"strings"
	"testing"
)

var configEnvironment = []string{
	"PORT",
	"DATABASE_URL",
	"SAP_API_URL",
	"HARDWARE_SYNC_DELAY_SECONDS",
	"SAP_TIMEOUT_MS",
	"SAP_ATTEMPTS_BEFORE_WAITING",
	"SAP_MAX_ATTEMPTS",
	"SAP_RECOVERY_WINDOW_SECONDS",
	"SAP_LISTENER_RECONNECT_MAX_MS",
	"WEBHOOK_SECRET",
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range configEnvironment {
		t.Setenv(name, "")
	}
}

func TestLoadUsesDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		clearConfigEnvironment(t)
		t.Setenv("DATABASE_URL", "postgresql://localhost/orders")
		t.Setenv("SAP_API_URL", "https://sap.example/api/orders")

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 3000 || cfg.HardwareSyncDelaySeconds != 30 || cfg.SAPTimeoutMS != 3000 || cfg.SAPMaxAttempts != DefaultSAPMaxAttempts || cfg.SAPMaxTotalAttempts != DefaultSAPMaxTotalAttempts || cfg.SAPRecoveryWindowSeconds != DefaultSAPRecoveryWindowSeconds || cfg.SAPListenerReconnectMaxMS != DefaultSAPListenerReconnectMaxMS {
			t.Fatalf("unexpected defaults: %+v", cfg)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		clearConfigEnvironment(t)
		t.Setenv("DATABASE_URL", "postgresql://db/orders")
		t.Setenv("SAP_API_URL", "https://sap.example/api/orders")
		t.Setenv("PORT", "8080")
		t.Setenv("HARDWARE_SYNC_DELAY_SECONDS", "45")
		t.Setenv("SAP_TIMEOUT_MS", "1500")
		t.Setenv("SAP_ATTEMPTS_BEFORE_WAITING", "4")
		t.Setenv("SAP_MAX_ATTEMPTS", "7")
		t.Setenv("SAP_RECOVERY_WINDOW_SECONDS", "90")
		t.Setenv("SAP_LISTENER_RECONNECT_MAX_MS", "750")
		t.Setenv("WEBHOOK_SECRET", "top-secret")

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 8080 || cfg.HardwareSyncDelaySeconds != 45 || cfg.SAPTimeoutMS != 1500 || cfg.SAPMaxAttempts != 4 || cfg.SAPMaxTotalAttempts != 7 || cfg.SAPRecoveryWindowSeconds != 90 || cfg.SAPListenerReconnectMaxMS != 750 || cfg.WebhookSecret != "top-secret" {
			t.Fatalf("unexpected overrides: %+v", cfg)
		}
	})
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		set     func(*testing.T)
		message string
	}{
		{name: "missing database", set: func(t *testing.T) { t.Setenv("DATABASE_URL", "") }, message: "DATABASE_URL is required"},
		{name: "missing SAP URL", set: func(t *testing.T) { t.Setenv("SAP_API_URL", "") }, message: "SAP_API_URL is required"},
		{name: "invalid SAP URL", set: func(t *testing.T) { t.Setenv("SAP_API_URL", "localhost:4000") }, message: "SAP_API_URL must be a valid URL"},
		{name: "invalid port", set: func(t *testing.T) { t.Setenv("PORT", "not-a-number") }, message: "numeric configuration values are invalid"},
		{name: "negative delay", set: func(t *testing.T) { t.Setenv("HARDWARE_SYNC_DELAY_SECONDS", "-1") }, message: "numeric configuration values are invalid"},
		{name: "zero SAP timeout", set: func(t *testing.T) { t.Setenv("SAP_TIMEOUT_MS", "0") }, message: "numeric configuration values are invalid"},
		{name: "zero SAP attempts", set: func(t *testing.T) { t.Setenv("SAP_ATTEMPTS_BEFORE_WAITING", "0") }, message: "numeric configuration values are invalid"},
		{name: "zero SAP max attempts", set: func(t *testing.T) { t.Setenv("SAP_MAX_ATTEMPTS", "0") }, message: "numeric configuration values are invalid"},
		{name: "zero SAP recovery window", set: func(t *testing.T) { t.Setenv("SAP_RECOVERY_WINDOW_SECONDS", "0") }, message: "numeric configuration values are invalid"},
		{name: "zero listener reconnect max", set: func(t *testing.T) { t.Setenv("SAP_LISTENER_RECONNECT_MAX_MS", "0") }, message: "numeric configuration values are invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("DATABASE_URL", "postgresql://localhost/orders")
			t.Setenv("SAP_API_URL", "https://sap.example/api/orders")
			test.set(t)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q, got %v", test.message, err)
			}
		})
	}
}

func TestLoadRejectsMalformedNumericValues(t *testing.T) {
	for _, name := range []string{"PORT", "HARDWARE_SYNC_DELAY_SECONDS", "SAP_TIMEOUT_MS", "SAP_ATTEMPTS_BEFORE_WAITING", "SAP_MAX_ATTEMPTS", "SAP_RECOVERY_WINDOW_SECONDS", "SAP_LISTENER_RECONNECT_MAX_MS"} {
		t.Run(name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("DATABASE_URL", "postgresql://localhost/orders")
			t.Setenv("SAP_API_URL", "https://sap.example/api/orders")
			t.Setenv(name, "not-an-integer")

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %v, want mention of %s", err, name)
			}
		})
	}
}
