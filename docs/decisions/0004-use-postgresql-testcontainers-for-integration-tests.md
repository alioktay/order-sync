# ADR 0004: Use PostgreSQL Testcontainers for integration tests

Status: Accepted

Integration tests use isolated PostgreSQL 17 Testcontainers so they exercise
the canonical migration and the same PostgreSQL behavior used in production,
including JSONB storage and transactional repository behavior. Unit tests use
fakes and `httptest` servers. SQLite is not implemented or supported as a
production or integration-test backend; adopting it would require a separate
design decision and dialect-specific verification.

[Back to README](../../README.md)
