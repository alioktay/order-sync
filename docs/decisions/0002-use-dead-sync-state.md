# ADR 0002: Keep DEAD distinct from retryable states

Status: Accepted

`DEAD` is a terminal synchronization outcome for a non-retryable SAP failure or an expired recovery window. `PENDING` and `WAITING` remain operationally retryable. Payment `FAILED` is unrelated: it records the provider’s business payment state and must not be confused with an exhausted SAP delivery. An operator may deliberately reopen a `DEAD` job after investigation with the admin CLI, which resets its delivery metadata and returns it to fresh `PENDING` work; this explicit operation does not make `DEAD` an automatically retryable state.
