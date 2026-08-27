package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/internal/orders"
	"order-sync/internal/payment"
	"order-sync/internal/shop"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/fx"
)

type HealthCheck func(context.Context) error

type requestIDContextKey struct{}

var requestSequence uint64

func NewHealthCheck(pool *pgxpool.Pool) HealthCheck {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

type RouterParams struct {
	fx.In
	Config  config.Config
	Shop    shop.API
	Payment payment.API
	Orders  orders.API
	Logger  *slog.Logger
	Health  HealthCheck
}

func NewRouter(p RouterParams) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestIDMiddleware(p.Logger))
	router.GET("/health", healthHandler(p.Health, p.Logger))
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	auth := webhookAuth(p.Config.WebhookSecret)
	router.POST("/api/webhooks/shop", auth, shopWebhookHandler(p.Shop, p.Logger))
	router.POST("/api/webhooks/payment", auth, paymentWebhookHandler(p.Payment, p.Logger))
	router.GET("/api/orders/:orderId", orderStatusHandler(p.Orders, p.Logger))
	return router
}

func requestIDMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strconv.FormatUint(atomic.AddUint64(&requestSequence, 1), 10)
		}
		c.Header("X-Request-ID", requestID)
		ctx := context.WithValue(c.Request.Context(), requestIDContextKey{}, requestID)
		c.Request = c.Request.WithContext(ctx)

		started := time.Now()
		c.Next()
		if logger != nil {
			logInfo(logger, ctx, "0002", "HTTP request", "request_id", requestID, "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "duration_ms", time.Since(started).Milliseconds())
		}
	}
}

// healthHandler handles the service liveness and database connectivity check.
// @Summary Service health
// @Description Returns OK when order-sync can reach PostgreSQL.
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /health [get]
func healthHandler(health HealthCheck, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := health(c.Request.Context()); err != nil {
			logError(logger, c.Request.Context(), "0003", "Health check failed", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func webhookAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret != "" && c.GetHeader("x-webhook-secret") != secret {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// shopWebhookHandler receives an order event from the shop.
// @Summary Receive shop order webhook
// @Tags webhooks
// @Accept json
// @Produce json
// @Param X-Webhook-Secret header string false "Shared webhook secret"
// @Param payload body shop.Webhook true "Shop order webhook"
// @Success 200 {object} contracts.WebhookResult
// @Success 201 {object} contracts.WebhookResult
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]string
// @Failure 409 {object} contracts.WebhookResult
// @Failure 500 {object} map[string]string
// @Security WebhookSecret
// @Router /api/webhooks/shop [post]
func shopWebhookHandler(api shop.API, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		processWebhook(c, logger, "0004", api.Process, shop.NormalizeShopWebhook, shop.ValidateShopWebhook, func(result contracts.WebhookResult) int {
			if result.Duplicate {
				return http.StatusOK
			}
			return http.StatusCreated
		})
	}
}

// paymentWebhookHandler receives a payment status event.
// @Summary Receive payment webhook
// @Tags webhooks
// @Accept json
// @Produce json
// @Param X-Webhook-Secret header string false "Shared webhook secret"
// @Param payload body payment.Webhook true "Payment webhook"
// @Success 200 {object} contracts.WebhookResult
// @Success 202 {object} contracts.WebhookResult
// @Failure 409 {object} contracts.WebhookResult
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security WebhookSecret
// @Router /api/webhooks/payment [post]
func paymentWebhookHandler(api payment.API, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		processWebhook(c, logger, "0005", api.Process, payment.NormalizePaymentWebhook, payment.ValidatePaymentWebhook, func(result contracts.WebhookResult) int {
			if result.Duplicate {
				return http.StatusOK
			}
			if result.Message == payment.MessageAwaitingShopOrder {
				return http.StatusAccepted
			}
			return http.StatusOK
		})
	}
}

func processWebhook[T any](c *gin.Context, logger *slog.Logger, logCode string, process func(context.Context, T) (contracts.WebhookResult, error), normalize func(T) T, validate func(T) []contracts.ValidationIssue, status func(contracts.WebhookResult) int) {
	var payload T
	if err := decode(c, &payload); err != nil {
		writeDecodeError(c, err)
		return
	}
	payload = normalize(payload)
	if issues := validate(payload); len(issues) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "issues": issues})
		return
	}
	result, err := process(c.Request.Context(), payload)
	if err != nil {
		writeWebhookProcessError(c, logger, c.Request.Context(), logCode, err)
		return
	}
	c.JSON(status(result), result)
}

func writeWebhookProcessError(c *gin.Context, logger *slog.Logger, ctx context.Context, code string, err error) {
	logError(logger, ctx, code, "Request failed", err)
	if errors.Is(err, contracts.ErrOrderPayloadConflict) || errors.Is(err, contracts.ErrPaymentPayloadConflict) {
		conflict := contracts.ErrOrderPayloadConflict
		if errors.Is(err, contracts.ErrPaymentPayloadConflict) {
			conflict = contracts.ErrPaymentPayloadConflict
		}
		c.JSON(http.StatusConflict, gin.H{"error": conflict.Error()})
		return
	}
	if errors.Is(err, contracts.ErrPaymentFinalized) || errors.Is(err, contracts.ErrDigitalCancellation) || errors.Is(err, contracts.ErrOrderCannotCancel) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
}

// orderStatusHandler returns the current state of an order.
// @Summary Get order status
// @Tags orders
// @Produce json
// @Param orderId path string true "Order identifier"
// @Success 200 {object} contracts.OrderStatus
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/orders/{orderId} [get]
func orderStatusHandler(api orders.API, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := api.GetOrderStatus(c.Request.Context(), c.Param("orderId"))
		if err != nil {
			logError(logger, c.Request.Context(), "0006", "Request failed", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}
		if result == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

const maxWebhookBodySize = 256 << 10

var (
	errInvalidJSON       = errors.New("invalid JSON")
	errRequestBodyTooBig = errors.New("request body too large")
	errUnsupportedType   = errors.New("unsupported content type")
)

func decode(c *gin.Context, target any) error {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errUnsupportedType
	}

	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodySize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errRequestBodyTooBig
		}
		if strings.Contains(err.Error(), "unknown field") {
			return err
		}
		return errInvalidJSON
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errRequestBodyTooBig
		}
		return errInvalidJSON
	}
	return nil
}
func writeDecodeError(c *gin.Context, err error) {
	if errors.Is(err, errRequestBodyTooBig) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request body too large"})
		return
	}
	if errors.Is(err, errUnsupportedType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported Content-Type; expected application/json"})
		return
	}
	if strings.Contains(err.Error(), "unknown field") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "issues": []contracts.ValidationIssue{{Code: "unrecognized_keys", Path: []any{}, Message: err.Error()}}})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
}

func logInfo(logger *slog.Logger, ctx context.Context, code, message string, args ...any) {
	if logger != nil {
		args = append([]any{"log_code", code}, args...)
		logger.InfoContext(ctx, code+" "+message, args...)
	}
}

func logError(logger *slog.Logger, ctx context.Context, code, message string, err error) {
	if logger != nil {
		attributes := []any{"log_code", code, "error", err}
		if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok {
			attributes = append(attributes, "request_id", requestID)
		}
		logger.ErrorContext(ctx, code+" "+message, attributes...)
	}
}
