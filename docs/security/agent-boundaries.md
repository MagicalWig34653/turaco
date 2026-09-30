# Connector and Endpoint Agent Boundaries

## Connector Agent

Purpose: bridge hosted platform to explicitly allowed internal resources such as AD/LDAP/internal APIs.

Properties:
- outbound connection only by default;
- unique tenant/agent cryptographic identity;
- locally allowed capabilities such as `ldap.users.read`, `ldap.groups.read`, `ldap.authenticate`;
- no implicit `shell.execute`/PowerShell execution;
- cloud cannot silently enable a capability disabled locally;
- sync-only mode is preferred where interactive operations are not required.

## Endpoint Agent

Purpose: device identity, inventory, software/OS/hardware observation and later typed management/remediation operations.

It is intentionally a separate binary/trust boundary because its privileges are materially higher.

Initial typed operations may include `CollectInventory`; later operations may include package install/update/remove and diagnostics. Remote Control is a future separate security design, not a side effect of generic command execution.

## Command requirements

Every privileged command includes command ID, agent/device/tenant identity, capability, issuer/system context, issued/expiry time and correlation/audit context. Duplicate delivery is safe and expired commands never execute.

## Remote support

Future remote support should use a dedicated broker/relay/session protocol with technician identity, RBAC/MFA/policy, user consent by default, unattended policy for explicit device classes, session audit and no reusable permanent session token.
