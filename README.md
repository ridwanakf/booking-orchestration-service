# Booking Orchestration Service

A service that sits between travel distributors and hotel suppliers, owns booking state, and stays correct when the supplier does not answer.

The happy path is not the interesting part. The interesting part is what happens when a supplier times out after receiving the request, answers twice, or confirms ten minutes later. Two sentences carry the whole design:

- **PostgreSQL decides what is true. Temporal decides what to do next.**
- **Unless we can prove the supplier never received the request, the outcome is ambiguous and must never become `FAILED` or `REJECTED`.**

The full design rationale is in [docs/RFC.md](docs/RFC.md). This file is how to run it and what it guarantees.

## Run it

Requires Docker and Docker Compose.

```bash
make up      # postgres, temporal, temporal-ui, and the service
make logs    # follow the service
make down    # tear down, including volumes
```

| What | Where |
|---|---|
| API | http://localhost:8080 |
| Swagger UI | http://localhost:8080/swagger/index.html |
| Temporal UI | http://localhost:8233 |
| Postgres | `localhost:55432`, user/password/db all `booking` |

Port 55432 rather than 5432 so it does not collide with a local Postgres.

Without Docker: `make migrate && make run`, with a reachable Postgres and Temporal. `CALLBACK_TOKEN` is required and has no default; the service refuses to start without it.

```bash
make check            # swagger, vet, build, unit tests
make test             # unit tests, race detector on
make test-integration # needs the stack up: guarded SQL, constraints, the trigger
```

Readiness deliberately excludes Temporal. A booking commits its row and returns `201` while the orchestrator is down, and the sweep drains the backlog afterwards, so failing readiness on a Temporal outage would pull every replica out of rotation and turn a survivable outage into a total one. Orchestrator reachability is a separate signal.

## Authentication

Distributors authenticate per request with an API key, `bok_<keyId>_<secret>`, sent as a bearer token. Only an Argon2id hash is stored, so the secret is not recoverable from the database. The compose stack seeds one key for `distributor-001`:

```bash
export KEY='bok_demo01_local-demo-secret'
curl localhost:8080/bookings/<id> -H "Authorization: Bearer $KEY"
```

The credential decides the tenant. There is no `distributorId` field in the create payload, so a distributor cannot attribute a booking to anyone else, and reads are scoped to the caller. A booking that belongs to someone else answers `404`, not `403`, so the endpoint does not confirm that it exists.

Supplier callbacks are a separate trust boundary with their own shared token, `X-Callback-Token`, compared in constant time before any state is read.

## The five scenarios, end to end

The supplier is mocked in-process and picks its behaviour from a suffix on `roomTypeId`, so every failure path is reachable with `curl` alone.

| Suffix | Supplier behaviour |
|---|---|
| `-confirm` | confirms immediately |
| `-reject` | declines with a recognized decline code |
| `-timeout` | holds past the deadline, then delivers its callback **twice** |
| `-unclassified` | answers `200` with an error envelope and no decline code |
| anything else | confirms |

### 1. Confirmed

```bash
curl -X POST localhost:8080/bookings \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer bok_demo01_local-demo-secret' -d '{
  "idempotencyKey": "partner-1",
  "propertyId": "hotel-001", "roomTypeId": "room-deluxe-confirm",
  "checkIn": "2026-09-10", "checkOut": "2026-09-12",
  "guest": {"firstName": "Taro", "lastName": "Yamada"}}'
```

`201` with `status: RECEIVED`. Poll `GET /bookings/{id}` and it becomes `CONFIRMED` with a `supplierReference`.

### 2. Rejected

Same call with `"roomTypeId": "room-deluxe-reject"`. Settles as `REJECTED` with `failureReason: NO_AVAILABILITY`. Never retried: a business decline is an answer, not a failure.

### 3. Timeout, then late confirmation

Same call with `"roomTypeId": "room-deluxe-timeout"`. The supplier holds the connection past the deadline, so the booking goes into `UNKNOWN`, not `FAILED`. Its callback arrives afterwards and moves it to `CONFIRMED`.

Poll `GET /bookings/{id}` while it runs and the intermediate states are visible:

```
t= 0s  PENDING
t= 4s  PENDING
t= 8s  UNKNOWN                 the deadline passed with no answer
t=12s  CONFIRMED  MOCK-...     the late callback resolved it
```

Doubt starts at the deadline, not before it. A booking is `PENDING` while its call is in flight and only becomes `UNKNOWN` when the deadline passes with nothing to show for it.

The compose stack sets `SUPPLIER_DEADLINE=8s` and `MOCK_TIMEOUT_HOLD=25s` so this is observable in seconds. The production default deadline is 90s, and the hold must exceed the deadline or the call simply succeeds and nothing times out.

### 4. The same booking sent to the supplier twice

Every attempt reuses one supplier idempotency key derived from the booking id, and there is one workflow per booking id, so retries and sweeps converge on a single execution. The mock deduplicates on that key and returns the same reference.

### 5. The same callback delivered twice

The mock deliberately posts its callback twice. The first applies; the second returns `200` with `{"applied": false, "reason": "duplicate"}`. Dedupe is state-based, so no event id or ledger is needed.

```bash
curl -X POST localhost:8080/supplier/callbacks \
  -H 'Content-Type: application/json' -H "X-Callback-Token: $CALLBACK_TOKEN" \
  -d '{"bookingId": "<id>", "supplierReference": "<ref>", "supplierStatus": "CONFIRMED"}'
```

### 6. The distributor retries after a timeout

Repeat any create with the same `idempotencyKey` and payload: `200`, the original booking, header `Idempotent-Replayed: true`. Same key with a **different** payload: `422 idempotency_key_reused`, and nothing is created or changed.

Two identical creates racing each other resolve in the database, not in memory:

```bash
for i in $(seq 1 12); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/bookings \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer bok_demo01_local-demo-secret' \
    -d '{"idempotencyKey":"race-1", ...}' &
done; wait
```

One `201`, eleven `200`, one row.

## API

| Method | Path | Notes |
|---|---|---|
| `POST` | `/bookings` | `201` new, `200` replay, `422` key reused with a changed payload, `400` invalid, `401` no or bad key |
| `GET` | `/bookings/{bookingId}` | `200`, `404` (also when the booking belongs to another distributor), `401` no or bad key |
| `POST` | `/supplier/callbacks` | `200` applied or duplicate, `401` bad token, `404` unknown booking, `400` status outside the vocabulary, `409` conflicts a settled state |
| `GET` | `/healthz` | liveness only |
| `GET` | `/readyz` | pings the database, and only the database |

Errors share one envelope: `{"status","code","message","detail?"}`. `code` is stable and machine readable.

## State model

Seven states. `CANCELLED` is modelled and reserved for a cancellation flow this version does not implement.

| State | Meaning |
|---|---|
| `RECEIVED` | committed, nothing sent yet |
| `PENDING` | a supplier call is in flight, or has been made and answered nothing yet |
| `UNKNOWN` | a request may have reached the supplier; the outcome is unproven |
| `CONFIRMED` | the supplier holds the booking, with its reference |
| `REJECTED` | the supplier declined for a reason we recognize |
| `FAILED` | our own failure provably before any send, or every attempt proven not sent |
| `CANCELLED` | reserved |

Every transition is a guarded update (`UPDATE ... WHERE status = <expected>`) that returns the row's status, so a lost race is a named outcome rather than a silent overwrite. Settled states have no automatic exits.

**The invariant worth reading twice:** the write that authorizes a supplier call records an outstanding attempt in `in_flight_attempt` and increments the attempt counter **before any bytes leave**. Doubt lives in that marker, not in `status`, so a healthy booking runs `RECEIVED -> PENDING -> CONFIRMED` and never enters `UNKNOWN`. A crash while the marker is set resolves to `UNKNOWN`, because an attempt outstanding with no recorded outcome is exactly what unknown means. There is no edge back out: proof that one attempt never left says nothing about an earlier one that may have arrived.

## Architecture

```
delivery/rest -> service -> repository -> PostgreSQL
                    |
              orchestrator (seam) -> Temporal -> workflow -> activities -> supplier
```

- `internal/model` holds the state machine as data, so tests iterate every pair.
- `internal/repository` publishes the persistence contract; the guarded writes are part of it.
- `internal/orchestrator` is the seam between the domain and the workflow engine.
- `internal/workflow` holds no business state: every decision reads the row, so a replay, a restart, and a sweep-started run all reach the same place.
- `internal/sweep` starts workflows for rows that have gone quiet. It writes no state.

## Technology choices

| Choice | Why |
|---|---|
| **Temporal** | Retries, durable timers, and a bounded park window are the problem. Building that on a queue means writing schedulers, dedupe, and visibility by hand. Workflow-id uniqueness also gives execution dedupe for free. |
| **PostgreSQL as the source of truth** | Booking state is queried, constrained, and audited. The workflow engine holds execution history, never business state. |
| **A unique constraint, not a lock** | `UNIQUE (distributor_id, idempotency_key)` resolves concurrent creates across replicas and restarts. In-memory locks do neither. |
| **gin, pgx, goose, urfave/cli, slog, testify, uber-go/mock** | Boring, current, and each defensible in review. |
| **The mock supplier in-process** | Every scenario is reproducible from one clone with no fixtures. It is off by default and the compose stack opts in, because it answers unauthenticated on the API port. |

## Operations

A booking that exhausts its attempt budget while still in doubt parks with `needs_recovery` set and waits. Until the outcome recovery pass exists, an operator works those rows from this query.

**What needs attention, oldest first:**

```sql
SELECT id, distributor_id, status, supplier_attempts, in_flight_attempt,
       supplier_reference, updated_at
FROM bookings
WHERE needs_recovery
ORDER BY updated_at;
```

**The full story of one booking**, which is what to read before touching anything:

```sql
SELECT seq, occurred_at, event_type, from_status, to_status, attempt,
       supplier_status_code, supplier_reason
FROM booking_events
WHERE booking_id = '<id>'
ORDER BY seq;
```

**Resolving one.** Never `UPDATE bookings SET status = ...` by hand: that skips the lineage append and the two stop agreeing. Establish the truth at the supplier first, then apply it through the same guarded path the service uses, which is the callback endpoint:

```bash
curl -X POST localhost:8080/supplier/callbacks \
  -H 'Content-Type: application/json' -H "X-Callback-Token: $CALLBACK_TOKEN" \
  -d '{"bookingId": "<id>", "supplierReference": "<their ref>", "supplierStatus": "CONFIRMED"}'
```

That clears the recovery flag, appends the event, and resolves the booking exactly as a late supplier callback would.

**Health signals worth a dashboard:** the age of the oldest row with `needs_recovery`, the count of `UNKNOWN` by age bucket, and the task queue's schedule-to-start latency. The last one is the leading indicator of a saturated worker fleet, and it moves long before CPU does.

## Production design notes

**A supplier request times out. What do we return, and what happens next?**
`UNKNOWN`, never a rejection. A timeout after the request left is not evidence of anything, and telling a distributor "failed" invites a rebooking of a room the supplier may already hold. The booking retries on a bounded budget, capped at two creates because a book endpoint is contractually metered. Be precise about what that second attempt is: **it is a blind create, not a retrieve.** That is safe here only because the mock deduplicates on the reference we send. Against a real supplier that merely echoes it, retrieve-before-retry is the correct strategy, which is why outcome recovery is the first roadmap item rather than a refinement. If the budget runs out while doubt remains, the booking parks with `needs_recovery` set and waits for a callback or an operator, rather than being closed with a guess.

**The distributor retries with the same idempotency key.**
`200` with the booking's current state and `Idempotent-Replayed: true`, never a second booking. Same key with a different payload is `422`: that is a client bug and answering with the original booking would hide it. A request fingerprint, a SHA-256 over the parsed fields with the key excluded, is what distinguishes the two.

**The supplier confirms minutes after the request timed out.**
An authenticated callback applies the outcome through the same guarded transition (`UNKNOWN -> CONFIRMED`), and the row is what the distributor's next `GET` returns. The workflow is signalled so a parked run finishes early, but the row transition is primary: a late callback resolves the booking even if the execution has already closed. A redelivery is a no-op; a callback contradicting a settled state is refused, logged, and flagged for recovery rather than applied.

**How do pending or unknown bookings survive a restart?**
Nothing lives in memory. Bookings are rows and workflows are durable, so a restart resumes. The gap a restart can open is between a committed row and a started workflow, and a sweep closes it: one query over unsettled rows that have gone quiet, restarting them by booking id. The recovery flag is not a trapdoor: it removes a row from the sweep only while that row is `UNKNOWN`, the parked case it exists for. A flagged `RECEIVED` or `PENDING` row is still swept, because otherwise one refused callback could strand a booking permanently. Because the workflow id is the booking id, restarting something already running is harmless. Every run also dispatches on the row's current status before acting, so a resumed or swept run never assumes it is the first.

**What changes with several instances running concurrently?**
Nothing in the design, which is the point. Correctness lives in the database constraint, the guarded updates, and workflow-id uniqueness, all of which are already cross-process. The task queue distributes work; two instances racing the same booking produce one winner and one named conflict. There are no in-memory locks to make cluster-safe, and no leader to elect. The sweep is safe to run on every instance because it only asks for a workflow to exist.

**What would change at millions of bookings and hundreds of suppliers?**
A supplier registry, so deadlines, retry safety, decline codes, and rate limits are per-supplier configuration rather than constants. Per-supplier task queues with rate governors, because book endpoints are contractually metered and one slow supplier must not starve the rest. An outcome-recovery pass that retrieves by our reference, which matters more than create idempotency since most hotel suppliers echo a client reference rather than deduplicating on it. Partitioning bookings by time, and moving the sweep to a partial index on unsettled rows. Metrics before dashboards: transitions by from/to, supplier latency by outcome, the age of the oldest parked `UNKNOWN`, and task-queue schedule-to-start latency, which is the leading indicator of a saturated worker fleet.

## Scope, simplifications, and next steps

Deliberately out of scope, each with a reason:

| Not built | Why |
|---|---|
| Pricing, payments, ledger | This service orchestrates booking state, not money. |
| Availability search | The supplier is the source of availability truth at booking time; a rejection covers "no rooms". |
| Real supplier integrations | The client is an interface; this version ships the mock behind it. |
| Rate limiting and quotas per distributor | The authentication seam is the natural place for it, but metering is a separate concern from booking correctness. |
| Cancellation and amendment | Designed in full for additive integration (a `CANCELLING` state, durable compensation, supplier-truth callbacks). Amendment needs cancellation first, because most bedbank channels force cancel-and-rebook. |
| Repriced or partial confirmation | A supplier confirming at a different rate, or confirming some rooms, is a commercial decision needing price, currency, occupancy, and a rate plan. This payload deliberately carries none of them, and modelling it half-way would be worse than refusing it. |
| HMAC callback signatures | The static shared token is the first thing to replace. Authentication here is defence in depth, not the correctness mechanism: replaying an authenticated payload cannot corrupt state, because processing is idempotent. |

Known limitations:

- The mock supplier deduplicates on the reference we send. **Most real hotel suppliers do not**: they echo and index a client reference instead. Against those, a blind retry is unsafe and retrieve-before-retry is the correct strategy, which is why outcome recovery is the first roadmap item rather than a refinement.
- The park timer is a fixed 24 hours. In production it should be the sooner of that and the booking's free-cancellation deadline, since past that deadline an unseen duplicate stops being refundable. This payload carries no rate plan or cancellation policy, so that rule has no data to work from here.
- `Flag` writes `updated_at = updated_at` intending to leave the sweep's staleness clock alone, but `x IS NOT DISTINCT FROM x` is true, so the trigger fires and the clock moves anyway. Flagged rows are kept out of the sweep by `FindStale`'s own predicate, so nothing is stranded, but the statement does not have the property it was written for. `TestFlaggingMovesTheStalenessClock` pins the real behaviour.
- A booking whose workflow fails on every start is restarted indefinitely. It surfaces as the age of the oldest `RECEIVED` row, so it is visible rather than silent, but nothing bounds it automatically.
- Distributor API keys are authenticated per request against an Argon2id hash, with no caching. That is a deliberate cost for correctness at this size, and a verified-key cache is the first thing to add under load. There is no rate limiting.
