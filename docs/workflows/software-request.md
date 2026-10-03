# Software Request

## Goal
Employee requests software without knowing deployment technology.

## Flow
1. Catalog Item selects a canonical SoftwareProduct from the approved software list (Software Approval Status) and eligibility/licensing context.
2. Approval/licensing checks run as configured.
3. Fulfillment determines the path: an Intune app published through a Software Management Provider (IntuneGet first, [ADR-0027](../decisions/ADR-0027-software-management-providers.md)), another Management Provider, or manual. A native Endpoint Agent/WinGet provider is a later option, not the default.
4. Platform creates desired state/deployment against the affected Device/User target where supported.
5. Deployment results update fulfillment. Manual fulfillment uses a Task with recorded outcome.
6. Desired State, provider-reported Management Assignment, expected applicability and observed result (Management Observation, corroborated by Software Installation) remain distinct.
7. Request completes on observed/recorded fulfillment according to policy.
