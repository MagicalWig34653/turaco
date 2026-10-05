# Infrastructure Change

**Status:** steps 1 (Initiatives including Changes, F7d), 2-4, 6 (Tasks only, no Runbook link) and 7 are implemented in the backend (F7a-F7d; [design](../product/f7-infrastructure-change-design.md#slice-4-status), [current status](../product/current-status.md)); step 5 has the maintenance calendar read model (`GET /api/v1/maintenance-calendar`) and the F8c backend Briefing feed from `planning/public`. The feed frontend remains delegated.

## Goal
Plan and execute infrastructure work with dependencies, physical context, communication and history.

## Flow
1. Initiative may describe a larger modernization; concrete work is represented by Changes.
2. Change relates to affected Assets/VMs/Services/Sites/Racks and risk/rollback/maintenance window.
3. Relationship graph supports impact analysis (informational confidence must be visible).
4. Approval occurs according to change type/risk.
5. Scheduled Change appears in IT Briefing/calendar and can generate employee/IT notifications for affected scopes.
6. Runbook/Tasks guide execution; physical work records relevant Rack/Asset timeline entries.
7. Change completes/reviews/closes or fails with rollback status.
8. Infrastructure documentation is updated as part of the same change when reality changed.
