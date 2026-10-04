# Security Remediation

**Status:** F8a backend implements Advisory ingestion, normalization, matching, Finding triage and fresh-observation re-verification. The UI and steps involving Briefing aggregation, Tasks, Changes and Deployments remain planned ([design](../product/f8-security-briefing-design.md), [current status](../product/current-status.md)).

## Goal
Turn external/internal advisories into evidence-based operational remediation tied to actual inventory.

## Flow
1. Security Advisory is entered by hand or imported from a bounded JSON source and normalized to affected software/OS/version criteria. F8a records applicability explicitly and notifies `security.manage` holders when an Advisory becomes applicable.
2. The `security.match` job compares criteria with observed Endpoint installations through `endpoints/public`, creating or updating Vulnerability Findings with `probable` or `potential` confidence. `potential` is never presented as confirmed. The six-hour `security.match_all` pass requeues matching; each Advisory run is capped at 20,000 Devices and reports truncation.
3. IT investigates, accepts, plans remediation, marks a false positive or accepts risk with a reason and review date. A User needs the separate `security.accept_risk` permission to accept risk. Advisory/Finding summaries and filtered lists expose affected counts and concrete Devices to authorized readers; Device names need `endpoints.view`.
4. F8c will show applicable Advisories in the computed IT Briefing and link counts to filtered lists. The manual Briefing Items already implemented in F2 remain separate records.
5. Approved remediation creates Change/maintenance context when policy requires and a Deployment of an approved Software Version; packaging and publishing run through a Software Management Provider (IntuneGet → Intune first, [ADR-0027](../decisions/ADR-0027-software-management-providers.md)).
6. Deployment Rings: a pilot ring precedes broad rollout; promotion needs approval and success on fresh observed evidence.
7. F8a marks Findings remediated only on newer observations that no longer match or on a Device/installation tombstone. Provider publish success and Task completion are not evidence of remediation. F8b/F9 will add the Task/Deployment response to correlated failures.
8. F8a shows Finding state counts per Advisory and records a risk acceptance's actor, reason and review date. F8b/F8c add richer progress/residual-risk views and the computed Briefing entry.
