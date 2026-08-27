package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

const (
	DefaultSAPMaxAttempts            = 3
	DefaultSAPMaxTotalAttempts       = 5
	DefaultSAPRecoveryWindowSeconds  = 15 * 60
	DefaultSAPListenerReconnectMaxMS = 1000
)

type Config struct {
	Port                      int
	DatabaseURL               string
	SAPAPIURL                 string
	HardwareSyncDelaySeconds  int
	SAPTimeoutMS              int
	SAPMaxAttempts            int
	SAPMaxTotalAttempts       int
	SAPRecoveryWindowSeconds  int
	SAPListenerReconnectMaxMS int
	WebhookSecret             string
}

func Load() (Config, error) {
	port, err := intEnv("PORT", 3000)
	if err != nil {
		return Config{}, err
	}
	hardwareDelay, err := intEnv("HARDWARE_SYNC_DELAY_SECONDS", 30)
	if err != nil {
		return Config{}, err
	}
	sapTimeout, err := intEnv("SAP_TIMEOUT_MS", 3000)
	if err != nil {
		return Config{}, err
	}
	maxAttempts, err := intEnv("SAP_ATTEMPTS_BEFORE_WAITING", DefaultSAPMaxAttempts)
	if err != nil {
		return Config{}, err
	}
	maxTotalAttempts, err := intEnv("SAP_MAX_ATTEMPTS", DefaultSAPMaxTotalAttempts)
	if err != nil {
		return Config{}, err
	}
	recoveryWindow, err := intEnv("SAP_RECOVERY_WINDOW_SECONDS", DefaultSAPRecoveryWindowSeconds)
	if err != nil {
		return Config{}, err
	}
	reconnectMax, err := intEnv("SAP_LISTENER_RECONNECT_MAX_MS", DefaultSAPListenerReconnectMaxMS)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Port:                      port,
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		SAPAPIURL:                 os.Getenv("SAP_API_URL"),
		HardwareSyncDelaySeconds:  hardwareDelay,
		SAPTimeoutMS:              sapTimeout,
		SAPMaxAttempts:            maxAttempts,
		SAPMaxTotalAttempts:       maxTotalAttempts,
		SAPRecoveryWindowSeconds:  recoveryWindow,
		SAPListenerReconnectMaxMS: reconnectMax,
		WebhookSecret:             os.Getenv("WEBHOOK_SECRET"),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if c.SAPAPIURL == "" {
		return Config{}, fmt.Errorf("SAP_API_URL is required")
	}
	u, err := url.ParseRequestURI(c.SAPAPIURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, fmt.Errorf("SAP_API_URL must be a valid URL")
	}
	if c.Port <= 0 || c.HardwareSyncDelaySeconds < 0 || c.SAPTimeoutMS <= 0 || c.SAPMaxAttempts <= 0 || c.SAPMaxTotalAttempts <= 0 || c.SAPRecoveryWindowSeconds <= 0 || c.SAPListenerReconnectMaxMS <= 0 {
		return Config{}, fmt.Errorf("numeric configuration values are invalid")
	}
	return c, nil
}

func intEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("numeric configuration values are invalid: %s must be an integer: %w", name, err)
	}
	return parsed, nil
}
