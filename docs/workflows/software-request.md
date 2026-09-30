# Software Request

## Goal
Employee requests software without knowing deployment technology.

## Flow
1. Catalog Item selects canonical SoftwareProduct and eligibility/licensing context.
2. Approval/licensing checks run as configured.
3. Fulfillment determines provider (Intune, Endpoint Agent/WinGet, manual) behind a Management Provider adapter.
4. Platform creates desired state/deployment against the affected Device/User target where supported.
5. Deployment results update fulfillment. Manual fulfillment uses a Task with recorded outcome.
6. Installed Software observation remains distinct from Software Assignment/desired state.
7. Request completes on observed/recorded fulfillment according to policy.
