# Security Remediation

## Goal
Turn external/internal advisories into evidence-based operational remediation tied to actual inventory.

## Flow
1. Security Advisory ingested/published and normalized to affected software/OS/version criteria.
2. Software/endpoint observations create Vulnerability Findings with confidence; "potential" is not presented as confirmed vulnerability.
3. IT analyzes applicability and publishes relevant Briefing Item showing affected count.
4. Query/Dynamic Group exposes concrete affected endpoints; dashboard count is clickable.
5. Approved remediation creates Change/maintenance context when policy requires and a Deployment of an approved Software Version; packaging and publishing run through a Software Management Provider (IntuneGet → Intune first, [ADR-0027](../decisions/ADR-0027-software-management-providers.md)).
6. Deployment Rings: a pilot ring precedes broad rollout; promotion needs approval and success on fresh observed evidence.
7. Fresh provider observations (not publish success) move Findings toward remediated; correlated failures create Tasks or Tickets.
8. Advisory/Briefing shows progress and residual risk. Risk acceptance records actor/reason/review date.
