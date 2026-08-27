# order-sync

`order-sync` owns the durable order-sync service and PostgreSQL schema for the example e-commerce flow. It accepts shop and payment webhooks, reconciles events that arrive out of order, and delivers paid orders to SAP through a transactional outbox worker.

## Repository boundary

This repository owns PostgreSQL, migrations, the order-sync API, and the SAP delivery worker. order-sync-helper repository owns the dashboard, mock SAP, and Adminer. The two Compose projects share the fixed `order-sync-shared` Docker network for local demonstrations.

## Quick start

Prerequisites: Go 1.26+, Docker Desktop, and (for integration tests) a working Docker engine.

```powershell
docker compose up -d --build
Invoke-WebRequest -UseBasicParsing http://localhost:3000/health
```

The service listens on `http://localhost:3000`; PostgreSQL is exposed at `localhost:5432`. The default `SAP_API_URL` points to `mock-sap`, which is provided by the optional sibling `order-sync-helper` stack; start that stack on the shared `order-sync-shared` network before exercising SAP delivery. Without it, the API and database still start, but SAP delivery will retry and eventually enter recovery/dead states.

```powershell
go test ./...
go test -race ./...
make lint
make integration
make swagger
make compose-config
make build
```

The integration suite starts PostgreSQL 17 with Testcontainers. Unit tests use fakes; SQLite is not a supported or tested production substitute.

## Configuration

`SAP_MAX_ATTEMPTS` defaults to `5` and caps total SAP delivery attempts, including the initial attempt.

`DATABASE_URL` and `SAP_API_URL` are required by the application. Compose reads `DATABASE_URL` from the project environment and passes it into the container unchanged; use the `postgres` hostname for Compose or `localhost` when running directly on the host. Compose also supplies a default SAP URL. Optional application values are `PORT` (3000), `HARDWARE_SYNC_DELAY_SECONDS` (30), `SAP_TIMEOUT_MS` (3000), `SAP_ATTEMPTS_BEFORE_WAITING` (3), `SAP_RECOVERY_WINDOW_SECONDS` (900), `SAP_LISTENER_RECONNECT_MAX_MS` (1000), and `WEBHOOK_SECRET` (empty, disabling webhook authentication). Compose also uses `POSTGRES_DB` (`order_sync`), `POSTGRES_USER` (`order_sync`), `POSTGRES_PASSWORD` (`order_sync`), and `POSTGRES_PORT` (5432).

See the complete [configuration and operations reference](docs/operations.md).

## Architecture and behavior

HTTP handlers validate and normalize events, then run business changes in a database transaction. Orders, payments, webhook event IDs, and `sync_jobs` are stored in PostgreSQL. A job is created in the same transaction as the paid order state; a worker claims due jobs with row locking, calls SAP, and records success or retry state. Hardware-containing orders use the configured persisted due time; actual SAP delivery may be slightly later because of worker, database, and network latency. Digital-only orders are due immediately.

Read [architecture](docs/architecture.md) for components and failure handling, and [domain behavior](docs/domain.md) for statuses, reconciliation, idempotency, and cancellation.

## API and examples

- [API contract](docs/api.md)
- [Swagger UI](http://localhost:3000/swagger/index.html) when the service runs
- Checked-in [Swagger YAML](docs/swagger.yaml) and [Swagger JSON](docs/swagger.json)
- [Shop example](examples-requests/shop-order.json), [payment example](examples-requests/payment.json)
- [Postman collection](postman/order-sync.postman_collection.json) and [environment](postman/order-sync.local.postman_environment.json)

Regenerate checked-in Swagger artifacts with `make swagger` after changing handler annotations or API contracts.

## Operations

See [operations](docs/operations.md) for Compose deployment, migrations, backups, health checks, logs, stale jobs, and troubleshooting. For public webhook testing, follow the existing [ngrok runbook](docs/ngrok-live-interview.md) or run `.\scripts\ngrok.ps1`.

To replay a terminal synchronization job after investigation, use the admin CLI via `make db-replay JOB_ID=<id>`. It accepts only a job currently in `DEAD` state and returns it to fresh `PENDING` work; see the [DEAD-job replay runbook](docs/operations.md#replay-a-dead-synchronization-job).

Architectural decisions are indexed in [docs/decisions](docs/decisions/README.md).
