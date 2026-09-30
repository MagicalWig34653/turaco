# Security Remediation

## Goal
Turn external/internal advisories into evidence-based operational remediation tied to actual inventory.

## Flow
1. Security Advisory ingested/published and normalized to affected software/OS/version criteria.
2. Software/endpoint observations create Vulnerability Findings with confidence; "potential" is not presented as confirmed vulnerability.
3. IT analyzes applicability and publishes relevant Briefing Item showing affected count.
4. Query/Dynamic Group exposes concrete affected endpoints; dashboard count is clickable.
5. Approved remediation creates Change/maintenance context when policy requires and a Deployment through the appropriate provider.
6. Pilot ring precedes broad rollout where configured.
7. Deployment results and fresh observations move Findings toward remediated; failures create Tasks.
8. Advisory/Briefing shows progress and residual risk. Risk acceptance records actor/reason/review date.
