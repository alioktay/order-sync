package sync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type workerTestJobs struct {
	mu             sync.Mutex
	claimJob       *Job
	claimed        bool
	claimErr       error
	nextWake       *time.Time
	nextWakeErr    error
	watchErr       error
	watchCalls     int
	wakeups        chan struct{}
	listenerErrors chan error
	claimReady     bool
	syncedID       int64
	sapID          string
	markSyncedErr  error
	failedID       int64
	failedStatus   contracts.SyncStatus
	failedDueAt    time.Time
	failedError    string
	markFailedErr  error
	syncedSignal   chan struct{}
}

func (j *workerTestJobs) ClaimDue(context.Context) (*Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.claimErr != nil {
		return nil, j.claimErr
	}
	if j.claimed {
		return nil, nil
	}
	if j.claimJob != nil && j.nextWake != nil && time.Now().Before(*j.nextWake) && !j.claimReady {
		return nil, nil
	}
	j.claimed = true
	if j.claimJob != nil {
		j.nextWake = nil
	}
	return j.claimJob, nil
}
func (j *workerTestJobs) NextWake(context.Context) (*time.Time, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.nextWake, j.nextWakeErr
}
func (j *workerTestJobs) Watch(context.Context) (<-chan struct{}, <-chan error, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.watchCalls++
	if j.watchErr != nil {
		return nil, nil, j.watchErr
	}
	if j.wakeups == nil {
		j.wakeups = make(chan struct{}, 1)
	}
	if j.listenerErrors == nil {
		j.listenerErrors = make(chan error, 1)
	}
	return j.wakeups, j.listenerErrors, nil
}
func (j *workerTestJobs) MarkSynced(_ context.Context, id int64, sapID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.syncedID, j.sapID = id, sapID
	if j.syncedSignal != nil {
		close(j.syncedSignal)
	}
	return j.markSyncedErr
}
func (j *workerTestJobs) MarkFailed(_ context.Context, id int64, status contracts.SyncStatus, dueAt time.Time, message string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.failedID, j.failedStatus, j.failedDueAt, j.failedError = id, status, dueAt, message
	return j.markFailedErr
}

func (j *workerTestJobs) setClaimReady(ready bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.claimReady = ready
}

type workerTestSAP struct {
	sapID string
	err   error
}

type classifiedTestError struct {
	err       error
	retryable bool
}

func (e classifiedTestError) Error() string   { return e.err.Error() }
func (e classifiedTestError) Unwrap() error   { return e.err }
func (e classifiedTestError) Retryable() bool { return e.retryable }

type retryAfterTestError struct {
	err   error
	delay time.Duration
	valid bool
}

func (e retryAfterTestError) Error() string                     { return e.err.Error() }
func (e retryAfterTestError) Retryable() bool                   { return true }
func (e retryAfterTestError) RetryAfter() (time.Duration, bool) { return e.delay, e.valid }

func (s workerTestSAP) SyncOrder(context.Context, string, OrderDetails) (string, error) {
	return s.sapID, s.err
}

type workerCountingSAP struct {
	calls int
	err   error
}

func (s *workerCountingSAP) SyncOrder(context.Context, string, OrderDetails) (string, error) {
	s.calls++
	return "", s.err
}

// concurrentDeliverySAP and injectedPersistenceJobs deliberately model the
// two independent systems around deliver. They make the SAP-success/DB-fail
// window deterministic while still allowing the deliveries to race.
type concurrentDeliverySAP struct {
	calls atomic.Int64
}

func (s *concurrentDeliverySAP) SyncOrder(_ context.Context, orderID string, _ OrderDetails) (string, error) {
	s.calls.Add(1)
	return "SAP-" + orderID, nil
}

type injectedPersistenceJobs struct {
	markSyncedCalls atomic.Int64
	markFailedCalls atomic.Int64
	failMarkSynced  atomic.Int64

	mu       sync.Mutex
	failed   map[int64]struct{}
	synced   map[int64]string
	statuses map[int64]contracts.SyncStatus
}

func newInjectedPersistenceJobs(failMarkSynced int64) *injectedPersistenceJobs {
	jobs := &injectedPersistenceJobs{
		failed:   make(map[int64]struct{}),
		synced:   make(map[int64]string),
		statuses: make(map[int64]contracts.SyncStatus),
	}
	jobs.failMarkSynced.Store(failMarkSynced)
	return jobs
}

func (j *injectedPersistenceJobs) ClaimDue(context.Context) (*Job, error)       { return nil, nil }
func (j *injectedPersistenceJobs) NextWake(context.Context) (*time.Time, error) { return nil, nil }
func (j *injectedPersistenceJobs) Watch(context.Context) (<-chan struct{}, <-chan error, error) {
	return make(chan struct{}), make(chan error), nil
}
func (j *injectedPersistenceJobs) MarkSynced(_ context.Context, id int64, sapID string) error {
	j.markSyncedCalls.Add(1)
	if j.failMarkSynced.Load() > 0 && j.failMarkSynced.Add(-1) >= 0 {
		return errors.New("injected database update failure")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.synced[id] = sapID
	return nil
}
func (j *injectedPersistenceJobs) MarkFailed(_ context.Context, id int64, status contracts.SyncStatus, _ time.Time, _ string) error {
	j.markFailedCalls.Add(1)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.failed[id] = struct{}{}
	j.statuses[id] = status
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type countingHandler struct {
	mu     sync.Mutex
	infos  int
	errors int
}

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level >= slog.LevelError {
		h.errors++
	} else {
		h.infos++
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func countingLogger() (*slog.Logger, *countingHandler) {
	h := &countingHandler{}
	return slog.New(h), h
}

func TestDeliverMarksSuccessfulJobSynced(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-123"}, config.Config{SAPMaxAttempts: 3}, testLogger())

	worker.deliver(context.Background(), Job{ID: 7, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-7"}})

	if jobs.syncedID != 7 || jobs.sapID != "SAP-123" {
		t.Fatalf("unexpected synced result: id=%d sapID=%q", jobs.syncedID, jobs.sapID)
	}
	if jobs.failedID != 0 {
		t.Fatalf("successful job was marked failed: id=%d", jobs.failedID)
	}
}

func TestDeliverSkipsDeadJob(t *testing.T) {
	jobs := &workerTestJobs{}
	sap := &workerCountingSAP{}
	worker := NewWorker(jobs, sap, config.Config{}, testLogger())
	worker.deliver(context.Background(), Job{ID: 1, Status: contracts.SyncStatusDead, Attempts: 9, OrderDetails: OrderDetails{OrderID: "dead"}})
	if sap.calls != 0 || jobs.failedID != 0 || jobs.syncedID != 0 {
		t.Fatalf("dead job was processed: SAP calls=%d failed=%d synced=%d", sap.calls, jobs.failedID, jobs.syncedID)
	}
}

func TestDeliverLogsSchedulingLag(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, nil))
	dueAt := time.Now().Add(-150 * time.Millisecond)
	worker := NewWorker(&workerTestJobs{}, workerTestSAP{sapID: "SAP-LAG"}, config.Config{SAPMaxAttempts: 3}, logger)

	worker.deliver(context.Background(), Job{Attempts: 1, DueAt: &dueAt, OrderDetails: OrderDetails{OrderID: "order-lag"}})

	if !strings.Contains(buffer.String(), `"schedule_lag_ms":`) {
		t.Fatalf("dispatch log = %s, want schedule_lag_ms", buffer.String())
	}
}

func TestDeliverSchedulesRetryAndEntersWaiting(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{err: errors.New("SAP unavailable")}, config.Config{SAPMaxAttempts: 3}, testLogger())
	before := time.Now()

	worker.deliver(context.Background(), Job{ID: 8, Attempts: 2, OrderDetails: OrderDetails{OrderID: "order-8"}})

	if jobs.failedStatus != "PENDING" || jobs.failedError != "SAP unavailable" {
		t.Fatalf("unexpected retry state: status=%q error=%q", jobs.failedStatus, jobs.failedError)
	}
	if jobs.failedDueAt.Before(before) || jobs.failedDueAt.After(before.Add(2*time.Second+250*time.Millisecond)) {
		t.Fatalf("retry due time %s is outside expected range", jobs.failedDueAt)
	}

	worker.deliver(context.Background(), Job{ID: 8, Attempts: 3, OrderDetails: OrderDetails{OrderID: "order-8"}})
	if jobs.failedStatus != "WAITING" {
		t.Fatalf("got threshold status %q, want WAITING", jobs.failedStatus)
	}
}

func TestDeliverStopsRetryingAtTotalAttemptLimit(t *testing.T) {
	jobs := &workerTestJobs{}
	sap := &workerCountingSAP{err: errors.New("SAP unavailable")}
	worker := NewWorker(jobs, sap, config.Config{SAPMaxAttempts: 3, SAPMaxTotalAttempts: 5}, testLogger())
	worker.deliver(context.Background(), Job{ID: 19, Attempts: 5, OrderDetails: OrderDetails{OrderID: "order-19"}})
	if jobs.failedStatus != contracts.SyncStatusDead || sap.calls != 1 {
		t.Fatalf("final allowed attempt = calls %d, status %q", sap.calls, jobs.failedStatus)
	}

	jobs = &workerTestJobs{}
	sap = &workerCountingSAP{err: errors.New("SAP unavailable")}
	worker = NewWorker(jobs, sap, config.Config{SAPMaxAttempts: 3, SAPMaxTotalAttempts: 5}, testLogger())
	worker.deliver(context.Background(), Job{ID: 20, Attempts: 6, OrderDetails: OrderDetails{OrderID: "order-20"}})
	if jobs.failedStatus != contracts.SyncStatusDead || sap.calls != 0 {
		t.Fatalf("delivery beyond limit = calls %d, status %q", sap.calls, jobs.failedStatus)
	}
}

func TestDeliverSynchronizesReplayedDeadJob(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-REPLAYED"}, config.Config{SAPMaxAttempts: 3}, testLogger())
	dueAt := time.Now()
	worker.deliver(context.Background(), Job{
		ID: 18, Status: contracts.SyncStatusPending, Attempts: 0, DueAt: &dueAt, OrderDetails: OrderDetails{OrderID: "order-replayed"},
	})
	if jobs.syncedID != 18 || jobs.sapID != "SAP-REPLAYED" {
		t.Fatalf("replayed job sync = id %d SAP %q, want id 18 SAP-REPLAYED", jobs.syncedID, jobs.sapID)
	}
	if jobs.failedID != 0 {
		t.Fatalf("replayed job unexpectedly failed: id %d status %q error %q", jobs.failedID, jobs.failedStatus, jobs.failedError)
	}
}

func TestDeliverMarksNonRetryableSAPErrorDeadImmediately(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{err: classifiedTestError{err: errors.New("SAP rejected order"), retryable: false}}, config.Config{SAPMaxAttempts: 3}, testLogger())
	before := time.Now()

	worker.deliver(context.Background(), Job{ID: 16, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-16"}})

	if jobs.failedStatus != contracts.SyncStatusDead || jobs.failedError != "SAP rejected order" {
		t.Fatalf("unexpected terminal state: status=%q error=%q", jobs.failedStatus, jobs.failedError)
	}
	if jobs.failedDueAt.Before(before) || jobs.failedDueAt.After(time.Now().Add(250*time.Millisecond)) {
		t.Fatalf("non-retryable error was scheduled for a future retry: %s", jobs.failedDueAt)
	}
}

func TestDeliverRetriesClassifiedSAPError(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{err: classifiedTestError{err: errors.New("SAP unavailable"), retryable: true}}, config.Config{SAPMaxAttempts: 3}, testLogger())
	before := time.Now()

	worker.deliver(context.Background(), Job{ID: 17, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-17"}})

	if jobs.failedStatus != contracts.SyncStatusPending || jobs.failedError != "SAP unavailable" {
		t.Fatalf("unexpected retry state: status=%q error=%q", jobs.failedStatus, jobs.failedError)
	}
	if jobs.failedDueAt.Before(before) || jobs.failedDueAt.After(before.Add(time.Second+250*time.Millisecond)) {
		t.Fatalf("classified retry due time %s is outside expected range", jobs.failedDueAt)
	}
}

func TestDeliverRetriesWaitingJobAndCanRecover(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{err: errors.New("SAP unavailable")}, config.Config{SAPMaxAttempts: 3, SAPRecoveryWindowSeconds: 30}, testLogger())
	waitingSince := time.Now()
	before := time.Now()
	worker.deliver(context.Background(), Job{ID: 10, Status: "WAITING", Attempts: 4, WaitingSince: &waitingSince, OrderDetails: OrderDetails{OrderID: "order-10"}})
	if jobs.failedStatus != "WAITING" {
		t.Fatalf("got waiting retry status %q, want WAITING", jobs.failedStatus)
	}
	if jobs.failedDueAt.Before(before) || jobs.failedDueAt.After(before.Add(8*time.Second+250*time.Millisecond)) {
		t.Fatalf("waiting retry due time %s is outside expected range", jobs.failedDueAt)
	}

	jobs = &workerTestJobs{}
	worker = NewWorker(jobs, workerTestSAP{sapID: "SAP-10"}, config.Config{SAPMaxAttempts: 3, SAPRecoveryWindowSeconds: 30}, testLogger())
	worker.deliver(context.Background(), Job{ID: 10, Status: "WAITING", Attempts: 5, WaitingSince: &waitingSince, OrderDetails: OrderDetails{OrderID: "order-10"}})
	if jobs.syncedID != 10 || jobs.sapID != "SAP-10" || jobs.failedID != 0 {
		t.Fatalf("waiting job did not recover: synced=%d sap=%q failed=%d", jobs.syncedID, jobs.sapID, jobs.failedID)
	}
}

func TestDeliverMarksExpiredWaitingJobDeadWithoutCallingSAP(t *testing.T) {
	jobs := &workerTestJobs{}
	sap := &workerCountingSAP{err: errors.New("SAP unavailable")}
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, nil))
	worker := NewWorker(jobs, sap, config.Config{SAPMaxAttempts: 3, SAPRecoveryWindowSeconds: 1}, logger)
	waitingSince := time.Now().Add(-2 * time.Second)

	worker.deliver(context.Background(), Job{ID: 15, Status: "WAITING", Attempts: 7, WaitingSince: &waitingSince, OrderDetails: OrderDetails{OrderID: "order-15"}})

	if sap.calls != 0 {
		t.Fatalf("expired waiting job called SAP %d times", sap.calls)
	}
	if jobs.failedStatus != "DEAD" || jobs.failedError == "" {
		t.Fatalf("expired waiting job = status %q error %q, want DEAD with an explanation", jobs.failedStatus, jobs.failedError)
	}
	if !strings.Contains(buffer.String(), `"msg":"0014 SAP synchronization marked DEAD"`) || !strings.Contains(buffer.String(), `"log_code":"0014"`) || !strings.Contains(buffer.String(), `"status":"DEAD"`) {
		t.Fatalf("terminal log = %s, want a DEAD status signal", buffer.String())
	}
}

func TestDeliverRetriesWhenPersistingSuccessFails(t *testing.T) {
	jobs := &workerTestJobs{markSyncedErr: errors.New("database unavailable")}
	worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-456"}, config.Config{SAPMaxAttempts: 3}, testLogger())

	worker.deliver(context.Background(), Job{ID: 9, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-9"}})

	if jobs.failedStatus != "PENDING" || jobs.failedError != "database unavailable" {
		t.Fatalf("unexpected recovery state: status=%q error=%q", jobs.failedStatus, jobs.failedError)
	}
}

func TestDeliverMarksPersistFailureDeadAtMaximumAttempts(t *testing.T) {
	jobs := &workerTestJobs{markSyncedErr: errors.New("database unavailable")}
	worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-456"}, config.Config{SAPMaxAttempts: 2, SAPMaxTotalAttempts: 2}, testLogger())
	worker.deliver(context.Background(), Job{ID: 9, Attempts: 2, OrderDetails: OrderDetails{OrderID: "order-9"}})
	if jobs.failedStatus != contracts.SyncStatusDead || jobs.failedError != "database unavailable" {
		t.Fatalf("unexpected terminal recovery state: status=%q error=%q", jobs.failedStatus, jobs.failedError)
	}
}

func TestDeliverRetriesAfterInjectedDatabaseUpdateFailure(t *testing.T) {
	jobs := newInjectedPersistenceJobs(1)
	sap := &concurrentDeliverySAP{}
	worker := NewWorker(jobs, sap, config.Config{SAPMaxAttempts: 3}, testLogger())
	job := Job{ID: 101, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-injected"}}

	worker.deliver(context.Background(), job)
	worker.deliver(context.Background(), Job{ID: job.ID, Attempts: 2, OrderDetails: job.OrderDetails})

	if got := sap.calls.Load(); got != 2 {
		t.Fatalf("SAP calls = %d, want 2 after retry", got)
	}
	if got := jobs.markSyncedCalls.Load(); got != 2 {
		t.Fatalf("MarkSynced calls = %d, want 2", got)
	}
	if got := jobs.markFailedCalls.Load(); got != 1 {
		t.Fatalf("MarkFailed calls = %d, want 1 for the injected failure", got)
	}
	jobs.mu.Lock()
	status := jobs.statuses[job.ID]
	sapID := jobs.synced[job.ID]
	jobs.mu.Unlock()
	if status != contracts.SyncStatusPending || sapID != "SAP-order-injected" {
		t.Fatalf("recovery state = status %q SAP %q, want PENDING and final SAP ID", status, sapID)
	}
}

func TestDeliverConcurrentLoadWithSAPSuccessDatabaseFailure(t *testing.T) {
	const deliveries = 128
	jobs := newInjectedPersistenceJobs(deliveries)
	sap := &concurrentDeliverySAP{}
	worker := NewWorker(jobs, sap, config.Config{SAPMaxAttempts: 3}, testLogger())

	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(deliveries)
	for i := 0; i < deliveries; i++ {
		id := int64(i + 1)
		go func() {
			defer group.Done()
			<-start
			worker.deliver(context.Background(), Job{ID: id, Attempts: 1, OrderDetails: OrderDetails{OrderID: "load-order"}})
		}()
	}
	close(start)
	group.Wait()

	if got := sap.calls.Load(); got != deliveries {
		t.Fatalf("SAP calls = %d, want %d", got, deliveries)
	}
	if got := jobs.markSyncedCalls.Load(); got != deliveries {
		t.Fatalf("MarkSynced calls = %d, want %d", got, deliveries)
	}
	if got := jobs.markFailedCalls.Load(); got != deliveries {
		t.Fatalf("MarkFailed calls = %d, want %d after DB failures", got, deliveries)
	}
	jobs.mu.Lock()
	failed, badStatuses := len(jobs.failed), 0
	for _, status := range jobs.statuses {
		if status != contracts.SyncStatusPending {
			badStatuses++
		}
	}
	jobs.mu.Unlock()
	if failed != deliveries || badStatuses != 0 {
		t.Fatalf("failure-injection results = failed %d, bad statuses %d; want %d failed PENDING jobs", failed, badStatuses, deliveries)
	}
}

func TestWorkerStartAndStop(t *testing.T) {
	worker := NewWorker(&workerTestJobs{}, workerTestSAP{}, config.Config{SAPMaxAttempts: 3}, testLogger())
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() before Start() = %v", err)
	}

	worker.Start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
}

func TestWorkerStartIsIdempotent(t *testing.T) {
	jobs := &workerTestJobs{}
	worker := NewWorker(jobs, workerTestSAP{}, config.Config{}, testLogger())
	var group sync.WaitGroup
	for range 8 {
		group.Go(worker.Start)
	}
	group.Wait()
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs.watchCalls != 1 {
		t.Fatalf("Watch() calls = %d, want exactly one", jobs.watchCalls)
	}
}

func TestWorkerTimerDispatchesAtPersistedDueTime(t *testing.T) {
	dueAt := time.Now().Add(30 * time.Millisecond)
	jobs := &workerTestJobs{claimJob: &Job{ID: 13, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-13"}}, nextWake: &dueAt, syncedSignal: make(chan struct{})}
	worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-13"}, config.Config{SAPMaxAttempts: 3}, testLogger())
	worker.Start()
	defer func() { _ = worker.Stop(context.Background()) }()

	select {
	case <-jobs.syncedSignal:
	case <-time.After(time.Second):
		t.Fatal("worker did not dispatch the job from its persisted timer")
	}
}

func TestWorkerNotificationWakesBeforeTimer(t *testing.T) {
	dueAt := time.Now().Add(time.Hour)
	wakeups := make(chan struct{}, 1)
	j := &workerTestJobs{
		claimJob:       &Job{ID: 14, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-14"}},
		nextWake:       &dueAt,
		wakeups:        wakeups,
		listenerErrors: make(chan error, 1),
		syncedSignal:   make(chan struct{}),
	}
	worker := NewWorker(j, workerTestSAP{sapID: "SAP-14"}, config.Config{SAPMaxAttempts: 3}, testLogger())
	worker.Start()
	defer func() { _ = worker.Stop(context.Background()) }()

	select {
	case <-j.syncedSignal:
		t.Fatal("worker dispatched a future job without a notification")
	case <-time.After(25 * time.Millisecond):
	}
	j.setClaimReady(true)
	wakeups <- struct{}{}
	select {
	case <-j.syncedSignal:
	case <-time.After(time.Second):
		t.Fatal("worker did not dispatch after receiving a notification")
	}
}

func TestRetryDelayMS(t *testing.T) {
	for _, test := range []struct{ attempt, want int }{{1, 1000}, {4, 8000}, {7, 60000}, {20, 60000}} {
		if got := RetryDelayMS(test.attempt); got != test.want {
			t.Fatalf("attempt %d: got %d, want %d", test.attempt, got, test.want)
		}
	}
}

func TestRetryDelayUsesRetryAfterAndCapsIt(t *testing.T) {
	if got := retryDelay(retryAfterTestError{err: errors.New("429"), delay: 7 * time.Second, valid: true}, 1); got != 7*time.Second {
		t.Fatalf("retryDelay() = %s, want 7s", got)
	}
	if got := retryDelay(retryAfterTestError{err: errors.New("429"), delay: 2 * time.Minute, valid: true}, 1); got != 60*time.Second {
		t.Fatalf("retryDelay() = %s, want 60s cap", got)
	}
	if got := retryDelay(retryAfterTestError{err: errors.New("429"), delay: 7 * time.Second, valid: false}, 1); got < 0 || got > time.Second {
		t.Fatalf("retryDelay() = %s, want jittered exponential fallback between 0 and 1s", got)
	}
}

func TestWorkerStopHonorsContext(t *testing.T) {
	worker := &Worker{cancel: func() {}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() = %v, want context cancellation", err)
	}
}

func TestWorkerTickBranches(t *testing.T) {
	t.Run("claim failure", func(t *testing.T) {
		logger, counts := countingLogger()
		worker := NewWorker(&workerTestJobs{claimErr: errors.New("claim failed")}, workerTestSAP{}, config.Config{SAPMaxAttempts: 3}, logger)
		worker.tick(context.Background())
		if counts.errors != 1 {
			t.Fatalf("error logs = %d, want 1", counts.errors)
		}
	})

	t.Run("delivers claimed job", func(t *testing.T) {
		logger, counts := countingLogger()
		jobs := &workerTestJobs{claimJob: &Job{ID: 11, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-11"}}}
		worker := NewWorker(jobs, workerTestSAP{sapID: "SAP-11"}, config.Config{SAPMaxAttempts: 3}, logger)
		worker.tick(context.Background())
		if jobs.syncedID != 11 || counts.infos != 1 {
			t.Fatalf("synced ID = %d, info logs = %d", jobs.syncedID, counts.infos)
		}
	})

	t.Run("ignores overlapping tick", func(t *testing.T) {
		logger, _ := countingLogger()
		worker := NewWorker(&workerTestJobs{claimErr: errors.New("must not be called")}, workerTestSAP{}, config.Config{}, logger)
		worker.running.Store(true)
		worker.tick(context.Background())
	})
}

func TestWorkerLoopHandlesListenerAndScheduleFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger, counts := countingLogger()
	jobs := &workerTestJobs{watchErr: errors.New("listener unavailable"), nextWakeErr: errors.New("schedule unavailable")}
	done := make(chan struct{})
	NewWorker(jobs, workerTestSAP{}, config.Config{}, logger).loop(ctx, done)
	if counts.errors == 0 {
		t.Fatal("listener failure was not logged")
	}
}

func TestDeliverReportsMarkFailedError(t *testing.T) {
	logger, counts := countingLogger()
	jobs := &workerTestJobs{markFailedErr: errors.New("update failed")}
	worker := NewWorker(jobs, workerTestSAP{err: errors.New("SAP failed")}, config.Config{SAPMaxAttempts: 3}, logger)
	worker.deliver(context.Background(), Job{ID: 12, Attempts: 1, OrderDetails: OrderDetails{OrderID: "order-12"}})
	if counts.errors != 2 {
		t.Fatalf("error logs = %d, want 2", counts.errors)
	}
	if got := errorMessage(nil); got != "" {
		t.Fatalf("errorMessage(nil) = %q", got)
	}
}
