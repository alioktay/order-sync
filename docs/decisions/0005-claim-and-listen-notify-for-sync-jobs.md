# ADR 0005: Claim and LISTEN/NOTIFY for sync jobs

Status: Accepted

## Context

`sync_jobs` is a durable PostgreSQL-backed work queue for SAP delivery. The
worker must support concurrent instances, scheduled retries, and recovery after
a process or database connection failure. It also needs to react promptly when
a new job is created or a retry becomes due.

Long polling would require repeated database queries while no work is
available. A shorter polling interval improves responsiveness at the cost of
more database load, while a longer interval reduces load but delays delivery.

## Decision

Use PostgreSQL for both durable job state and dispatch coordination:

- The worker claims one due or stale job at a time with a transaction using
  `FOR UPDATE SKIP LOCKED`. The claim changes the job to `PROCESSING`, records
  the lock timestamp, increments the attempt count, and loads the job payload.
- A PostgreSQL trigger publishes the changed job ID on the `sync_jobs`
  `LISTEN/NOTIFY` channel after inserts and relevant status or due-time updates.
  The worker listens for notifications and drains all currently claimable jobs.
- Notifications are wakeup hints, not the source of truth. The worker also
  calculates the next persisted due time, reconnects the listener after
  failures, and reclaims stale `PROCESSING` jobs. This ensures correctness even
  when a notification is missed or a listener is disconnected.

## Why

Atomic claiming gives safe horizontal scaling without introducing a separate
queue service: concurrent workers skip rows already claimed by another worker.
`LISTEN/NOTIFY` avoids idle database polling and gives low-latency wakeups for
new work and retries. Keeping the job state and claim operation in PostgreSQL
preserves transactional consistency with the order and payment state already
stored there.

This combination separates responsiveness from correctness. Notifications
make normal delivery prompt, while persisted due times and stale-claim recovery
make delivery reliable across notification loss, worker restarts, and transient
database connection failures.

## Consequences

- The service remains dependent on PostgreSQL row locking and notification
  semantics, which is acceptable because PostgreSQL is the system of record.
- The worker must maintain a listener connection and handle reconnects.
- `LISTEN/NOTIFY` payloads must not be treated as durable queue messages; the
  worker must always reconcile against `sync_jobs`.
- No external queue infrastructure or polling interval tuning is required for
  ordinary dispatch.

## Alternatives rejected

- **Long polling or fixed-interval polling:** simpler conceptually, but creates
  unnecessary query load when idle and forces a responsiveness-versus-load
  tradeoff.
- **An external queue:** could provide dedicated queue semantics, but would add
  another durable system, synchronization concerns, and operational overhead
  for a workload already coordinated by PostgreSQL.
