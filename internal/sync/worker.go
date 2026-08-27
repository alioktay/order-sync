package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"sync"
	"sync/atomic"
	"time"
)

const (
	listenerReconnectMinDelay = 250 * time.Millisecond
)

type Worker struct {
	jobs    JobRepository
	sap     SapClient
	cfg     config.Config
	logger  *slog.Logger
	stateMu sync.Mutex
	started bool
	running atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewWorker(jobs JobRepository, sap SapClient, cfg config.Config, logger *slog.Logger) *Worker {
	return &Worker{jobs: jobs, sap: sap, cfg: cfg, logger: logger}
}
func (w *Worker) Start() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.started {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	go w.loop(ctx, w.done)
}
func (w *Worker) Stop(ctx context.Context) error {
	w.stateMu.Lock()
	cancel, done := w.cancel, w.done
	w.stateMu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *Worker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	wakeups, listenerErrors, err := w.jobs.Watch(ctx)
	if err != nil {
		w.logError(ctx, "0007", "SAP worker listener unavailable", slog.String("error", errorMessage(err)))
	}
	// Reconcile immediately on startup so jobs that became due during a restart
	// do not wait for a notification or the next timer transition.
	w.drainDue(ctx)
	reconnectDelay := listenerReconnectMinDelay
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var timerC <-chan time.Time
	for {
		if wakeups == nil {
			timerC = armTimer(timer, time.Now().Add(reconnectDelay))
		} else {
			nextWake, nextErr := w.jobs.NextWake(ctx)
			if nextErr != nil {
				w.logError(ctx, "0008", "SAP worker schedule reconciliation failed", slog.String("error", errorMessage(nextErr)))
				timerC = armTimer(timer, time.Now().Add(reconnectDelay))
			} else {
				timerC = armTimer(timer, derefTime(nextWake))
			}
		}

		select {
		case <-ctx.Done():
			return
		case _, ok := <-wakeups:
			if !ok {
				wakeups, listenerErrors = nil, nil
				continue
			}
			w.drainDue(ctx)
		case err, ok := <-listenerErrors:
			wakeups, listenerErrors = nil, nil
			if ok && err != nil {
				w.logError(ctx, "0009", "SAP worker listener failed", slog.String("error", errorMessage(err)))
			}
		case <-timerC:
			if wakeups == nil {
				wakeups, listenerErrors, err = w.jobs.Watch(ctx)
				if err != nil {
					w.logError(ctx, "0010", "SAP worker listener reconnect failed", slog.String("error", errorMessage(err)))
					reconnectDelay *= 2
					if reconnectDelay > w.listenerReconnectMaxDelay() {
						reconnectDelay = w.listenerReconnectMaxDelay()
					}
				} else {
					reconnectDelay = listenerReconnectMinDelay
				}
			} else {
				w.drainDue(ctx)
			}
		}
	}
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func armTimer(timer *time.Timer, wakeAt time.Time) <-chan time.Time {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	if wakeAt.IsZero() {
		return nil
	}
	delay := max(time.Until(wakeAt), 0)
	timer.Reset(delay)
	return timer.C
}

func (w *Worker) drainDue(ctx context.Context) {
	if !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	for {
		job, err := w.jobs.ClaimDue(ctx)
		if err != nil {
			w.logError(ctx, "0011", "SAP worker claim failed", slog.String("error", errorMessage(err)))
			return
		}
		if job == nil {
			return
		}
		w.deliver(ctx, *job)
	}
}

func (w *Worker) tick(ctx context.Context) { w.drainDue(ctx) }

func (w *Worker) deliver(ctx context.Context, job Job) {
	now := time.Now()
	if job.Status == contracts.SyncStatusDead {
		w.logTerminalJob(ctx, job)
		return
	}
	if w.recoveryWindowExpired(job, now) {
		w.expireRecoveryWindow(ctx, job, now)
		return
	}
	if job.Attempts > w.maxTotalAttempts() {
		message := fmt.Sprintf("SAP maximum attempts exceeded (%d)", w.maxTotalAttempts())
		if err := w.jobs.MarkFailed(ctx, job.ID, contracts.SyncStatusDead, now, message); err != nil {
			w.logError(ctx, "0021", "SAP job update failed", slog.String("orderId", job.OrderID), slog.String("error", errorMessage(err)))
		}
		w.logError(ctx, "0022", "SAP synchronization marked DEAD", slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts), slog.String("status", string(contracts.SyncStatusDead)), slog.String("reason", "max_attempts_exceeded"))
		return
	}

	w.logDispatch(ctx, job)
	dispatchStarted := time.Now()
	sapID, err := w.sap.SyncOrder(ctx, job.OrderID, job.OrderDetails)
	dispatchDuration := time.Since(dispatchStarted)
	if err != nil {
		w.handleDeliveryFailure(ctx, job, now, dispatchDuration, err)
		return
	}

	w.persistDeliverySuccess(ctx, job, now, dispatchDuration, sapID)
}

func (w *Worker) logTerminalJob(ctx context.Context, job Job) {
	w.logError(ctx, "0012", "SAP synchronization skipped for terminal job",
		slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts), slog.String("status", string(contracts.SyncStatusDead)))
}

func (w *Worker) expireRecoveryWindow(ctx context.Context, job Job, now time.Time) {
	message := fmt.Sprintf("SAP recovery window expired after %s", w.recoveryWindow())
	if err := w.jobs.MarkFailed(ctx, job.ID, contracts.SyncStatusDead, now, message); err != nil {
		w.logError(ctx, "0013", "SAP job update failed", slog.String("orderId", job.OrderID), slog.String("error", errorMessage(err)))
	}
	w.logError(ctx, "0014", "SAP synchronization marked DEAD",
		slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts), slog.String("status", string(contracts.SyncStatusDead)),
		slog.String("reason", "recovery_window_expired"), slog.String("error", message))
}

func (w *Worker) logDispatch(ctx context.Context, job Job) {
	if job.DueAt != nil {
		w.logInfo(ctx, "0020", "SAP synchronization dispatching",
			slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts),
			slog.Int64("schedule_lag_ms", time.Since(*job.DueAt).Milliseconds()))
	}
}

func (w *Worker) handleDeliveryFailure(ctx context.Context, job Job, now time.Time, duration time.Duration, err error) {
	retryable := isRetryable(err)
	status, dueAt := w.failureState(job, now, retryable, err)
	message := errorMessage(err)
	if markErr := w.jobs.MarkFailed(ctx, job.ID, status, dueAt, message); markErr != nil {
		w.logError(ctx, "0015", "SAP job update failed", slog.String("orderId", job.OrderID), slog.String("error", errorMessage(markErr)))
	}
	w.logError(ctx, "0016", "SAP synchronization failed",
		slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts), slog.String("status", string(status)),
		slog.Bool("waiting", status == contracts.SyncStatusWaiting), slog.Bool("dead", status == contracts.SyncStatusDead),
		slog.Int64("dispatch_duration_ms", duration.Milliseconds()), slog.String("dispatch_result", "failure"), slog.String("error", message))
}

func (w *Worker) failureState(job Job, now time.Time, retryable bool, err error) (contracts.SyncStatus, time.Time) {
	if !retryable {
		return contracts.SyncStatusDead, now
	}
	if job.Attempts >= w.maxTotalAttempts() {
		return contracts.SyncStatusDead, now
	}
	status := contracts.SyncStatusPending
	if job.Attempts >= w.maxAttempts() {
		status = contracts.SyncStatusWaiting
	}
	return status, now.Add(retryDelay(err, job.Attempts))
}

func (w *Worker) persistDeliverySuccess(ctx context.Context, job Job, now time.Time, duration time.Duration, sapID string) {
	if err := w.jobs.MarkSynced(ctx, job.ID, sapID); err != nil {
		status := contracts.SyncStatusPending
		if job.Attempts >= w.maxTotalAttempts() {
			status = contracts.SyncStatusDead
		}
		message := errorMessage(err)
		dueAt := now.Add(retryDelay(err, job.Attempts))
		if markErr := w.jobs.MarkFailed(ctx, job.ID, status, dueAt, message); markErr != nil {
			w.logError(ctx, "0017", "SAP job update failed", slog.String("orderId", job.OrderID), slog.String("error", errorMessage(markErr)))
		}
		w.logError(ctx, "0018", "SAP synchronization persistence failed",
			slog.String("orderId", job.OrderID), slog.Int("attempt", job.Attempts), slog.String("status", string(status)),
			slog.Bool("dead", status == contracts.SyncStatusDead), slog.String("error", message))
		return
	}
	w.logInfo(ctx, "0019", "Order synchronized with SAP",
		slog.String("orderId", job.OrderID), slog.String("sapInternalId", sapID),
		slog.Int64("dispatch_duration_ms", duration.Milliseconds()), slog.String("dispatch_result", "success"))
}

func (w *Worker) logInfo(ctx context.Context, code, message string, args ...any) {
	if w.logger != nil {
		args = append([]any{"log_code", code}, args...)
		w.logger.InfoContext(ctx, code+" "+message, args...)
	}
}

func (w *Worker) logError(ctx context.Context, code, message string, args ...any) {
	if w.logger != nil {
		args = append([]any{"log_code", code}, args...)
		w.logger.ErrorContext(ctx, code+" "+message, args...)
	}
}

func (w *Worker) maxAttempts() int {
	if w.cfg.SAPMaxAttempts <= 0 {
		return config.DefaultSAPMaxAttempts
	}
	return w.cfg.SAPMaxAttempts
}

func (w *Worker) maxTotalAttempts() int {
	if w.cfg.SAPMaxTotalAttempts <= 0 {
		return config.DefaultSAPMaxTotalAttempts
	}
	return w.cfg.SAPMaxTotalAttempts
}

func (w *Worker) listenerReconnectMaxDelay() time.Duration {
	maxMS := w.cfg.SAPListenerReconnectMaxMS
	if maxMS <= 0 {
		maxMS = config.DefaultSAPListenerReconnectMaxMS
	}
	return time.Duration(maxMS) * time.Millisecond
}

func (w *Worker) recoveryWindow() time.Duration {
	seconds := w.cfg.SAPRecoveryWindowSeconds
	if seconds <= 0 {
		seconds = config.DefaultSAPRecoveryWindowSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (w *Worker) recoveryWindowExpired(job Job, now time.Time) bool {
	return job.WaitingSince != nil && !now.Before(job.WaitingSince.Add(w.recoveryWindow()))
}

func RetryDelayMS(attempt int) int {
	delay := 1000
	for i := 1; i < attempt; i++ {
		if delay >= 60000 {
			return 60000
		}
		delay *= 2
	}
	if delay > 60000 {
		return 60000
	}
	return delay
}

func retryDelay(err error, attempt int) time.Duration {
	baseDelay := time.Duration(RetryDelayMS(attempt)) * time.Millisecond
	delay := jitteredDelay(baseDelay)
	var retryAfter interface{ RetryAfter() (time.Duration, bool) }
	if errors.As(err, &retryAfter) {
		if headerDelay, ok := retryAfter.RetryAfter(); ok && headerDelay >= 0 {
			delay = headerDelay
		}
	}
	const maxRetryDelay = 60 * time.Second
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// jitteredDelay spreads retries over the interval [0, baseDelay]. This avoids
// many jobs that failed together retrying at exactly the same instant.
func jitteredDelay(baseDelay time.Duration) time.Duration {
	if baseDelay <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(baseDelay) + 1))
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}

type retryability interface {
	Retryable() bool
}

func isRetryable(err error) bool {
	var classified retryability
	if errors.As(err, &classified) {
		return classified.Retryable()
	}
	return true
}
