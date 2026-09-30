# Employee Offboarding

## Goal
Safely reclaim assets/access and create an auditable exit process.

## Flow
1. Authoritative signal/manual initiation identifies User and departure date.
2. Platform resolves active Asset Assignments, relevant access records/requests and outstanding work.
3. Tasks are created for equipment return, account/access actions and exceptions.
4. Returned Assets move `assigned → returned`; inspection/provisioning decides `available`, `in_repair` or `retired`.
5. Endpoint management actions (retire/wipe/etc.) are explicit high-impact provider actions with authorization/audit.
6. Missing Assets become explicit exception/lost workflows, never silently closed.
7. User organizational status changes from source-of-truth sync; platform history remains.
