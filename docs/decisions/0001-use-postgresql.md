# ADR 0001: Use PostgreSQL

Status: Accepted

PostgreSQL is the durable production store because order/payment state and webhook payloads need transactional consistency, JSONB storage, unique keys, row locking, and `LISTEN/NOTIFY` for efficient sync-job wakeups. The canonical schema is the embedded `migrations/001_initial.sql` migration.

