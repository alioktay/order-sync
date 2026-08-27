# Operations

## Compose topology

`postgres` runs PostgreSQL 17 with a health check and named volume; `order-sync` waits for it, exposes port 3000, and runs the API plus worker. Both join `order-sync-shared`. The sibling `order-sync-helper` project adds dashboard, mock SAP, and Adminer.

```powershell
docker compose up -d --build
docker compose ps
docker compose logs -f order-sync
```

For a deliberately fresh local database:

```powershell
docker compose down -v
docker compose up -d --build order-sync
```

`down -v` deletes the local PostgreSQL volume and all local data. Do not use it for production or when data must be preserved.

## Environment reference

| Variable | Default | Required | Notes |
|---|---|---|---|
| `DATABASE_URL` | `postgresql://order_sync:order_sync@postgres:5432/order_sync` in Compose | yes | Application PostgreSQL URL; use `postgres` from Compose and `localhost` for direct host execution |
| `SAP_API_URL` | `http://mock-sap:4000/api/orders` in Compose | yes | Complete outbound URL |
| `PORT` | `3000` | no | Must be positive |
| `HARDWARE_SYNC_DELAY_SECONDS` | `30` | no | May be zero |
| `SAP_TIMEOUT_MS` | `3000` | no | Per-request timeout |
| `SAP_ATTEMPTS_BEFORE_WAITING` | `3` | no | Positive integer |
| `SAP_MAX_ATTEMPTS` | `5` | no | Hard limit for total SAP delivery attempts, including the initial attempt |
| `SAP_RECOVERY_WINDOW_SECONDS` | `900` | no | WAITING recovery period |
| `SAP_LISTENER_RECONNECT_MAX_MS` | `1000` | no | Maximum PostgreSQL listener reconnect backoff |
| `WEBHOOK_SECRET` | empty | no | Shared header secret |
| `POSTGRES_DB` | `order_sync` | Compose | Database container |
| `POSTGRES_USER` | `order_sync` | Compose | Database container |
| `POSTGRES_PASSWORD` | `order_sync` | Compose | Database container |
| `POSTGRES_PORT` | `5432` | Compose | Host port |

## Schema and backups

The application embeds and applies [001_initial.sql](../migrations/001_initial.sql). Use the admin image commands while PostgreSQL is running:

```powershell
make db-migrate
make db-verify
make db-shell
make db-backup
```

`db-verify` checks connectivity, required tables, and canonical SKU rows. `db-backup` writes `postgres-backup.sql` in the repository directory. Restore only with an approved change window, then verify:

```powershell
Get-Content .\postgres-backup.sql | docker compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
make db-verify
```

## Replay a DEAD synchronization job

An operator can explicitly replay a terminal synchronization job by job ID. The
command only accepts jobs currently in `DEAD` state; it resets delivery
attempts and prior SAP metadata, clears the error and recovery timestamps, and
makes the job immediately due as fresh `PENDING` work. The worker then applies
the normal SAP retry policy.

```powershell
make db-replay JOB_ID=123
```

Review the job’s `last_error` and payload, and check SAP-side idempotency history
first, before replaying. The operation is intended for an approved change
window. It does
not bypass cancellation or business-state rules, and a missing or non-`DEAD`
job is rejected.

## Health, logs, and correlation

Probe `GET /health`; 200 means the service can reach PostgreSQL, while 500 means it cannot. Application logs are JSON with stable `log_code` values 0001 through 0019 and request-scoped `request_id`. Codes 0002/0003 cover HTTP and health activity; 0007-0019 cover worker claim, retry, persistence, and SAP delivery. Follow a request with `docker compose logs -f order-sync` and its `X-Request-ID`.

## Troubleshooting

- Database unavailable: inspect `docker compose ps`, PostgreSQL health logs, `DATABASE_URL`, and `make db-verify`.
- SAP unavailable: verify `SAP_API_URL`, mock-SAP availability in the sibling stack, timeout, and worker log codes 0014/0016.
- Webhook 401: compare `WEBHOOK_SECRET` and `X-Webhook-Secret`; recreate the service after Compose environment changes.
- Webhook 400: inspect the returned validation `issues` and use the checked-in examples.
- Health 500: check PostgreSQL connectivity and application logs.
- Stuck `PROCESSING`: inspect `sync_jobs.locked_at`, `attempts`, `last_error`, and worker restart/reconciliation logs. Startup recovery makes stale claims due again; do not edit rows casually.
- `DEAD`: record order/job ID, attempts, timestamps, and last error; determine whether SAP rejected the payload or the recovery window expired. Use the explicit `make db-replay JOB_ID=<id>` operation only after reviewing the payload and SAP-side idempotency history. A missing or non-`DEAD` job is rejected.

Public webhook testing is covered by the [ngrok runbook](ngrok-live-interview.md).
