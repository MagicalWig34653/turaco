# Advisory feeds (NVD, CISA KEV)

**Status:** implemented, 2026-10. Decision B1 of the [F8 design](../product/f8-security-briefing-design.md). Both feeds are public and need no account. Rate limits and terms of use below are marked *verify*: they were taken from the vendors' public documentation and must be re-checked before production use.

## What exists

| Part | Location |
|---|---|
| Port (normalized records, `Syncer`, `KEVSource`, errors, `Fake`, `NotConfigured`) | `backend/internal/integrations/advisories` |
| NVD API 2.0 client | `backend/internal/integrations/advisories/nvd` |
| CISA KEV catalog client | `backend/internal/integrations/advisories/cisakev` |
| Sync job `security.advisory_sync`, KEV enrichment, feed state | `backend/internal/modules/security/application/feeds.go`, migration `000053_advisory_feeds.up.sql` |
| Wiring | `backend/internal/wiring/advisory_feeds.go`, `cmd/turaco-worker`, `turaco-admin security sync-feeds` |

Provider DTOs never leave the adapter packages. The sync imports through the regular advisory `Import` path as the system actor `advisory-feed`, so validation, idempotency, re-analysis of changed applicable advisories and audit are exactly those of a manual import. Imported advisories start in status `new`; Turaco never marks an advisory applicable on its own.

## Configuration

Registry: [configuration reference](../reference/configuration.md).

| Variable | Default | Meaning |
|---|---|---|
| `ADVISORY_SYNC` | `false` | Schedules the worker job. Off: nothing is scheduled; `turaco-admin security sync-feeds` still works. |
| `ADVISORY_SOURCES` | `nvd,cisa_kev` | Feeds to read. |
| `NVD_API_KEY_FILE` | empty | Optional file with an NVD API key. The key is read once by the worker, sent only as the `apiKey` header to the NVD API and never stored or logged. |
| `ADVISORY_SYNC_INTERVAL` | `6h` | Job interval, at least `1h`. |

Run once by hand: `turaco-admin security sync-feeds [--source nvd|cisa_kev] [--since YYYY-MM-DD]`. `--since` sets the NVD start for this run (backfill) and never moves the stored cursor backwards. The result (counts and error codes per source) is printed as JSON.

## NVD

- Endpoint `https://services.nvd.nist.gov/rest/json/cves/2.0`, incremental by `lastModStartDate`/`lastModEndDate`. Windows are at most 120 days (NVD's limit); a window that holds more records than the remaining per-run bound is halved down to one hour. Pagination uses `resultsPerPage` (default 500, at most 2000) and `startIndex`.
- The first run reads the last 30 days; later runs start at the stored cursor (end of the last completely read window).
- Per run at most 2,000 records (the bound stops the run; the cursor then marks where the next run continues).
- Rate limit *(verify)*: 5 requests per rolling 30 s without a key, 50 with a key. The client counts every attempt, including retries, and waits instead of exceeding it.
- Retries with exponential backoff (2 s base, 3 retries) on 403, 429 and 5xx and on network errors; a `Retry-After` header is honored, and one above two minutes ends the run with `rate_limited` instead of holding the worker.
- Per-request timeout 30 s, response size cap 32 MiB, `User-Agent: turaco/<version>`, cancellation through the context.
- Terms *(verify)*: NVD asks API users to follow its [terms of use](https://nvd.nist.gov/developers/terms-of-use) and to prefer a key and incremental updates for regular synchronization.

### Mapping

| Advisory field | From |
|---|---|
| source / external id | `nvd` / `CVE-…` |
| title | CVE id and the first English description (single line, cleaned, bounded to 300 characters) |
| summary | the English description (cleaned of control and bidirectional characters, at most 3,900 characters); up to ten https reference URLs are appended while the 4,000-character limit allows |
| severity | CVSS base score of the primary metric, preferring v4.0, then v3.1, then v3.0: 0 none, below 4 low, below 7 medium, below 9 high, otherwise critical; no score (not analyzed yet) is `none` and is updated when NVD scores it |
| published / modified | `published` / `lastModified` (UTC) |
| source URL | `https://nvd.nist.gov/vuln/detail/<id>` |
| criteria | `configurations` → `cpeMatch` entries with `vulnerable=true` and part `a` (application); `h` and `o` parts, non-vulnerable (context) matches and negated nodes are skipped. One criterion per vendor/product/target platform; vendor becomes the publisher, `_` becomes a space |

Version rules per `cpeMatch`: start-inclusive plus end-exclusive → `introduced`/`fixed`; end-exclusive only → `lt`; end-inclusive only → `le`; start only → `introduced`; exact CPE version → `eq`; version `*` or `-` without range → no rules (every version). A product with any unconstrained match gets no rules. Deliberate over-approximations that never hide an installation: a start-exclusive bound is treated as inclusive, and a range with an inclusive end becomes `le` (the lower bound is dropped). A match whose version contains unusual characters is skipped. At most 50 criteria and 20 rules per criterion are kept.

Not mapped: CVSS vectors and scores, CWE, EPSS, hardware and operating-system CPEs (an operating-system CVE therefore arrives without criteria and analysts decide), non-English descriptions, and references beyond the summary text. CVEs with `vulnStatus` `Rejected` are skipped; a CVE rejected after import stays as it was (analysts archive it). Advisories without usable criteria are still imported with empty criteria.

## CISA KEV

- Catalog `https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json`, fetched conditionally with `ETag`/`If-None-Match`; 30 s timeout, 8 MiB cap. Terms *(verify)*: the catalog is public CISA data; review the site's usage notice before production use.
- Entries become enrichment, not advisories: `known_exploited`, `known_exploited_added_at` and `kev_due_date` of matching advisories (by external id). They are set only by this enrichment, never by manual edit or the generic import, are audited (`security.advisory.kev_enriched`, ids only), and are never cleared by Turaco. `requiredAction` and `knownRansomwareCampaignUse` are parsed (bounded) but not stored yet.
- Known exploited advisories sort first in the briefing feed (critical severity, due date as due time), are counted in the security overview (`knownExploitedApplicable`) and show a badge with the due date in the advisory list and detail.
- KEV CVEs without an advisory are fetched from NVD by id (at most 50 per run, rate limited) and imported, then enriched. The stored ETag is kept only when every KEV CVE has an advisory, so an unfinished backlog is continued on the next run. The NVD `hasKev` filter could fetch them in one request; it is not used yet *(verify the parameter before relying on it)*.

## Operation

- State per source in `security.feed_state` (cursor, ETag, last success, last attempt, error code, lease). A run takes a lease so runs of one source never overlap (job and CLI included); the lease expires by itself when a worker dies.
- A failing source is recorded with a constant error code (`rate_limited`, `unavailable`, `invalid_response`, `timeout`, `import_failed`, `failed`) and does not stop the other source or fail the job; there is no error text, URL or key in the state, logs of the job or the briefing.
- The briefing integration health shows a warning per source with an error code or without a success for 48 hours.
- No network is needed for the test suite (httptest fixtures, fake clock). The live smoke tests run only with `TURACO_LIVE_FEED_TESTS=1`:

```bash
TURACO_LIVE_FEED_TESTS=1 go -C backend test -count=1 -run Live ./internal/integrations/advisories/
# optional: NVD_API_KEY_FILE=/path/to/key for the higher NVD rate limit
```

## Not built

OSV.dev, vendor RSS and Microsoft Security Update Guide (MSRC) adapters, persisting references and CVSS data, EPSS and exploit intelligence, and an admin UI for feed state.
