# Load testing (development only)

`tools/loadtest` is an open-loop load generator for the Turaco API. It signs in the simulated hospital users of [the hospital simulation](simulation-hospital.md), drives a persona-weighted workload at a scheduled arrival rate, measures latency, optionally samples PostgreSQL, checks data invariants afterwards and can delete what it created.

It is **development tooling**: it creates thousands of tickets and comments, writes to the shared development database and logs in with the public simulation password. It is not part of `make check` and is not a benchmark of a production deployment.

## Safety rules (enforced by the tool)

- The tool refuses to run unless `--base-url` points to `localhost`, `127.0.0.1` or `::1`, `GET /api/v1/meta` reports a development-like environment (`development`, `dev`, `local`, `test`), the process environment `APP_ENV` (when set) is development-like, and `--pg-url` (when given) points to a local database. `--i-know` overrides all of these. Never use it against a shared or production system.
- Everything it creates is tagged: ticket titles start with `[lt:<tag>]`, comments start with the same marker. The tag is printed at the start and in the report (`--tag` sets it).
- Writes touch only tickets that the run created itself. Pre-existing tickets (the 27 simulation tickets, tickets of manual testers) are only read.
- Audit events, notifications and queue number counters are not removed by cleanup (audit is append-only by design; counters never move backwards, so numbers of deleted tickets are never reused).
- The development database is shared with manual testers. Prefer a throwaway stack ([lab services](lab-services.md): database `turaco_lab`, API on `:18090`, `APP_ENV=development`) for anything above the smoke profile.

## Prerequisites

- API running with `AUTH_EMERGENCY_LOGIN_ENABLED=true`, built from the current source: routes that do not exist in an older build answer 404 without an error body. The report marks those operations ("route does not exist in this API build") and skips the dependent invariants instead of failing.
- The hospital simulation seeded (`turaco-admin demo seed-hospital`); `devadmin` exists for the admin persona.
- Optional: a PostgreSQL URL for sampling and SQL invariants (read-only access is enough for those; `cleanup` needs delete rights).

## Quick start

```bash
make load-test                                            # smoke: 20 operations/s for 5 s
make load-test LOADTEST_ARGS="--profile ramp --rate-scale 0.1 --pg-url postgres://turaco:turaco-dev@localhost:5432/turaco?sslmode=disable --cleanup"
go run ./tools/loadtest run -h                            # all flags
go run ./tools/loadtest cleanup --pg-url postgres://turaco:turaco-dev@localhost:5432/turaco?sslmode=disable --tag <tag>   # or --all, --dry-run
```

The report is printed to stdout and written to `tmp/loadtest/loadtest-<time>-<tag>.json` and `.txt` (`--out` changes the directory; `tmp/` is git-ignored). Progress lines go to stderr every 5 s. The exit code is 1 when the verdict is FAIL (`--no-fail` suppresses it) and 2 for usage or safety refusals.

## Arrival schedule

The load is **open loop**: arrivals are generated at their scheduled times whether or not earlier operations have finished. They are handed through a bounded queue (`--queue`) to a bounded worker pool (`--max-inflight`) that uses a bounded connection pool (`--max-conns`). When the server slows down, queues grow and **latency** rises; when the generator's queue is full the surplus arrivals are **dropped and counted**. The schedule itself never slows down, so overload shows up as latency, errors and drops instead of a generator that quietly backs off.

A schedule is a list of stages, `[name=][hold|ramp:]RATE@DURATION`, where the rate is in **operations per second** (one operation makes 1 to 3 HTTP requests):

| Stage | Meaning |
|---|---|
| `hold:500@30s` (or `500@30s`) | constant rate |
| `ramp:2000@20s` | linear from the previous stage's end rate to 2000 (the first stage of a schedule starts at its own rate) |
| `hold:0@10s` | cooldown: no arrivals, only draining |

Profiles (`--profile`, or `--stages` for a custom list; `--rate-scale` multiplies every rate):

| Profile | Schedule |
|---|---|
| `smoke` (default) | 20/s for 5 s |
| `ramp` | 50 → 500 → 2000 → 4000/s, each reached by a 10-20 s ramp and held for 20-30 s, then 10 s cooldown (about 4 minutes) |
| `spike` | 100/s, a 10 s spike to 2000/s, back to 100/s, cooldown |
| `soak` | ramp to 100/s, hold 10 minutes |

`--arrivals poisson` (default) uses exponential gaps that follow the rate; `uniform` spaces them evenly. `--seed` makes a run repeatable (it is printed and stored in the report).

## Workload

Personas follow the simulation: 32 logins in 7 classes. `--users N` takes the first N round-robin over the classes (0 = all 32), `--classes` restricts the classes, `--weights employee=60,technician=20,...` sets each class's share of arrivals (defaults: employee 50, firstlevel 14, technician 20, lead 8, viewer 3, admin 3, vendor 2). Sessions are kept; a 401 triggers a re-login and one retry (recorded as class `reauth`); sessions are also renewed after 55 minutes.

| Class | Logins | Operations |
|---|---|---|
| employee | 14 hospital staff | list own tickets, report a ticket, open and comment own tickets, knowledge search, catalog, queues for create, unread count |
| firstlevel, technician | `lena.bauer`, `murat.demir`; six `it-specialist` | `POST /tickets/query` with filter variants (open unassigned, new and urgent, title contains tag, text search, waiting, mine) with cursor paging, list all, open ticket, assign, public and internal comment, transitions (start, wait, resume, resolve, close, reopen), priority, move queue (only when two or more active Queues exist), knowledge, my-work, view counts |
| lead | three site leads | briefing feed, sidebar, view and queue counts, my-work, a few queries, opens, assigns, comments |
| viewer | `uwe.pohl`, `mirja.engel` (`tickets.view` only) | query, open, list, counts, and a write that must be denied |
| admin | `devadmin`, `ines.falk`, `deniz.arslan` | user list, audit search, queries, counts, sidebar, briefing |
| vendor | two `vendor-restricted` users | list own tickets, my-work, a probe that reads somebody else's ticket (must be denied), knowledge |

`403`/`404` answers on operations other than the two denial probes are reported as a note, because they usually mean the persona lacks the permission for that operation in this build.

### Writes and conflicts

Staff write operations are read-modify-write like a real client: `GET` the ticket, send the change with `expectedVersion` set to the version just read, and on `409 tickets.version_conflict` read again and retry (`--max-retries`, default 2). Every 409 is recorded on the write operation, retries and give-ups are counted, nothing is hidden. Staff pick the newest tickets (`--hot-tickets 50`, `--hot-prob 0.5`) so that several technicians work the same ticket and conflicts actually occur; lower `--hot-prob` for fewer conflicts. Tickets that reach `closed` or `cancelled` leave the pool, employees keep raising new ones (`--max-tickets` caps the total, default 20000; `--seed-tickets` raises the initial pool).

## Reading the results

- **Latency** (p50, p95, p99, max) is measured from the **scheduled** start, so queueing in the generator or in the server counts (coordinated omission). The `svc p99` column is measured from the moment the request was sent and shows how long the server needed once the request left. Only the first request of an operation is measured from the scheduled start; later requests of the same operation start when the previous one ended. Percentiles come from a log histogram with about 2 percent resolution; max is exact.
- **Outcome classes**: `ok` (2xx), `conflict` (409), `throttled` (429), `denied` (403/404), `reauth` (401), `invalid` (other 4xx: generator or contract problem), `server_error` (5xx), `network` (timeouts, resets, refusals). Only `server_error` and `network` fail the run; 409 and 429 are expected under load and reported separately, `invalid` is a warning.
- **Per stage** the report shows target, offered and finished operations per second, HTTP requests per second, latency, error classes and saturation signals: dropped arrivals, a backlog (fewer than 95 percent of the offered operations finished inside the stage), p99 above `--slo-p99` (default 1 s), 5xx or timeouts, more than 5 percent throttled. "Saturation first seen in stage ..." names the first saturated stage. A one-second timeline (offered, started, finished, dropped, in-flight, queue depth, errors, window p50/p99) is in the JSON.
- **Generator health**: dispatch lag (how late arrivals were released), peak in-flight and queue depth, goroutines. A dispatch lag p99 above 50 ms or any dropped arrival means the generator, not the server, ran out of capacity (the load generator shares the laptop with the API and PostgreSQL); lower the rate or raise `--max-inflight`/`--queue`/`--max-conns`.
- **PostgreSQL** (`--pg-url`): every `--pg-interval` (2 s) from `pg_stat_activity` (connections of the database, active, idle in transaction, sessions waiting for a lock, longest active query and transaction), `pg_locks` (ungranted), and the delta of `pg_stat_database` (commits and rollbacks per second, deadlocks, temp files, tuples, cache hit ratio). The report shows peaks against `max_connections` and the longest query text. Sampling uses one extra connection.

## Invariants (after the run)

| Check | Source |
|---|---|
| no 5xx, no timeouts or connection errors | recorded outcomes |
| no ticket reference issued twice among created tickets | API answers |
| no lost updates: two successful conditional writes never return the same new version; the version never goes down | API answers |
| no authorization leak: denial probes (foreign ticket read by a vendor, write by a read-only role) never succeed | API answers |
| created tickets resolve through `GET /tickets/by-reference` (also after a queue move: the old number is an alias) | API (sample of 100) |
| queue and view counts equal list counts of the query engine, per Queue and for "unassigned" (after `--settle`, default 16 s, because System View counts are cached for 15 s; capped or unavailable counts are skipped) | API, and SQL with `--pg-url` |
| final ticket versions are not below any observed version, no acknowledged ticket is missing | SQL with `--pg-url`, otherwise a sample of 200 via API |
| unique references, unique (queue, number), exactly one current `reference_registry` row per ticket that equals the ticket reference, queue counters ahead of issued numbers, created tickets exist | SQL, `--pg-url` only |

A failing invariant fails the run. A skipped check says why (for example no Queues listed by an older build).

## Rate limits and expected limits on a laptop

- The query engine limits **each principal** to 5 requests per second sustained with a burst of 30 (`backend/internal/platform/query/plan.go`). At most 16 simulation principals reach `POST /tickets/query`, so at most roughly 80 queries per second can pass in steady state however high the target rate is; the rest answers `429`. This is correct behavior: the 429s are counted in their own class and column and do not fail the run. Login also throttles per account and client; the tool honors `Retry-After`.
- To push more load through the limited endpoints: use all 32 users (`--users 0`, the default; the limiter is per principal, so a second session of the same login does not help), and shift the mix away from queries with `--weights` (for example `employee=70,lead=15,technician=10`). The `views.counts`, `sidebar`, `my-work` and ticket read/write operations are not behind that limiter.
- With the simulation's 32 users every request of one login goes through one principal; per-principal limits therefore also shape the conflict rate. A 429 on a write is not retried.
- Targets such as 2000 or 4000 operations per second (up to about 10000 HTTP requests per second) are **not expected to be sustained** on a development laptop: the generator, the API and PostgreSQL (Colima, for example 2 CPU / 12 GiB) share the machine. The point of the `ramp` profile is to find the stage where saturation signals appear. Start with `--rate-scale 0.05` to `0.25` and increase. No baseline has been measured yet; record the first measured limits for this hardware here when they exist.
- The default of 128 connections and 256 workers suits a laptop; the API's own connection pool and `max_connections` of PostgreSQL (100 by default) are the usual first walls (watch "connections peak" and "lock waiters peak").

## Cleanup

```bash
go run ./tools/loadtest cleanup --pg-url "$DATABASE_URL" --tag <tag> --dry-run   # count
go run ./tools/loadtest cleanup --pg-url "$DATABASE_URL" --tag <tag>             # delete
go run ./tools/loadtest cleanup --pg-url "$DATABASE_URL" --all                   # every load-test tag
```

Or pass `--cleanup` with `--pg-url` to `run`. Cleanup runs in one transaction: it deletes comments that start with the marker and tickets whose title starts with the marker; comments, reference registry rows and problem links of those tickets go with them (cascade). It refuses a non-local database and a non-development `APP_ENV` unless `--i-know` is given.

## Limits of the tool

- Operations depend on the data of the simulation (knowledge search terms, personas and their roles); a different dataset produces different 403/404 notes.
- It drives the API only; browser rendering and the frontend are not measured.
- Employees pick up routing through the default intake Queue; Queue creation and grants are not exercised.
- The invariants verify what the API and database show after the run; they do not prove the absence of races.
