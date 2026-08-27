# ADR 0003: Transactional outbox for SAP delivery

Status: Accepted

Create `sync_jobs` in the same database transaction that records the paid order state. This prevents committed business state without a delivery record. Delivery is at-least-once from the service’s perspective, uses an idempotency key, and persists `SYNCED` or retry state after the SAP call. If the service crashes after SAP accepts the request but before `SYNCED` is recorded, avoiding a duplicate depends on SAP honoring that key. PostgreSQL notification is a wakeup optimization; durable due-time polling and stale-claim recovery preserve correctness.
