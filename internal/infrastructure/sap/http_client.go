package sap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"order-sync/internal/config"
	"order-sync/internal/sync"
	"strconv"
	"strings"
	stdsync "sync"
	"time"
)

type requestError struct {
	err           error
	retryable     bool
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (e *requestError) Error() string                     { return e.err.Error() }
func (e *requestError) Unwrap() error                     { return e.err }
func (e *requestError) Retryable() bool                   { return e.retryable }
func (e *requestError) RetryAfter() (time.Duration, bool) { return e.retryAfter, e.hasRetryAfter }

func wrapError(err error, retryable bool) error {
	return &requestError{err: err, retryable: retryable}
}

func wrapErrorWithRetryAfter(err error, retryable bool, retryAfter time.Duration, hasRetryAfter bool) error {
	return &requestError{err: err, retryable: retryable, retryAfter: retryAfter, hasRetryAfter: hasRetryAfter}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > (1<<63-1)/int64(time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil || date.Before(now) {
		return 0, false
	}
	return date.Sub(now), true
}

func retryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func retryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

type HTTPClient struct {
	cfg        config.Config
	client     *http.Client
	inflight   map[string]*syncOrderCall
	inflightMu stdsync.Mutex
}

type syncOrderCall struct {
	done  chan struct{}
	body  []byte
	sapID string
	err   error
}

func NewClient(cfg config.Config) sync.SapClient {
	return &HTTPClient{
		cfg:      cfg,
		client:   &http.Client{Timeout: timeDuration(cfg.SAPTimeoutMS)},
		inflight: make(map[string]*syncOrderCall),
	}
}
func (c *HTTPClient) SyncOrder(parent context.Context, idempotencyKey string, order sync.OrderDetails) (sapID string, err error) {
	if idempotencyKey == "" {
		return "", wrapError(errors.New("SAP idempotency key is empty"), false)
	}
	body, err := marshalOrder(order)
	if err != nil {
		return "", err
	}
	call, owner, err := c.startCall(idempotencyKey, body)
	if err != nil {
		return "", err
	}
	if !owner {
		select {
		case <-call.done:
			return call.sapID, call.err
		case <-parent.Done():
			return "", parent.Err()
		}
	}
	defer func() {
		c.inflightMu.Lock()
		call.sapID, call.err = sapID, err
		delete(c.inflight, idempotencyKey)
		close(call.done)
		c.inflightMu.Unlock()
	}()

	return c.postOrder(parent, idempotencyKey, order, body)
}

func marshalOrder(order sync.OrderDetails) ([]byte, error) {
	body, err := json.Marshal(order)
	if err != nil {
		return nil, wrapError(fmt.Errorf("marshal SAP order %q: %w", order.OrderID, err), false)
	}
	return body, nil
}

func (c *HTTPClient) startCall(idempotencyKey string, body []byte) (*syncOrderCall, bool, error) {
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	if call, ok := c.inflight[idempotencyKey]; ok {
		if !bytes.Equal(call.body, body) {
			return nil, false, wrapError(fmt.Errorf("SAP idempotency key %q reused with a different order payload", idempotencyKey), false)
		}
		return call, false, nil
	}
	call := &syncOrderCall{done: make(chan struct{}), body: body}
	c.inflight[idempotencyKey] = call
	return call, true, nil
}

func (c *HTTPClient) postOrder(parent context.Context, idempotencyKey string, order sync.OrderDetails, body []byte) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeDuration(c.cfg.SAPTimeoutMS))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.SAPAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", wrapError(fmt.Errorf("create SAP request for order %q: %w", order.OrderID, err), false)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("idempotency-key", idempotencyKey)
	response, err := c.client.Do(req)
	if err != nil {
		return "", wrapError(fmt.Errorf("send SAP request for order %q: %w", order.OrderID, err), retryableTransportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	return parseResponse(response, order)
}

func parseResponse(response *http.Response, order sync.OrderDetails) (string, error) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := retryableStatus(response.StatusCode)
		if response.StatusCode == http.StatusTooManyRequests {
			retryAfter, hasRetryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
			return "", wrapErrorWithRetryAfter(fmt.Errorf("SAP returned HTTP %d", response.StatusCode), retryable, retryAfter, hasRetryAfter)
		}
		return "", wrapError(fmt.Errorf("SAP returned HTTP %d", response.StatusCode), retryable)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", wrapError(fmt.Errorf("read SAP response for order %q: %w", order.OrderID, err), retryableTransportError(err))
	}
	var result struct {
		Status        string `json:"status"`
		SAPInternalID string `json:"sap_internal_id"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return "", wrapError(fmt.Errorf("decode SAP response for order %q: %w", order.OrderID, err), false)
	}
	if result.Status != "success" {
		return "", wrapError(fmt.Errorf("SAP response status was %q, want %q", result.Status, "success"), false)
	}
	if result.SAPInternalID == "" {
		return "", wrapError(fmt.Errorf("SAP response did not contain sap_internal_id"), false)
	}
	return result.SAPInternalID, nil
}
