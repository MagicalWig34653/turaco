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

Initial typed operations may include `CollectInventory`; later operations may include diagnostics and, only if a native Software Management Provider is approved by its own ADR, package install/update/remove. The default software path is provider-based ([ADR-0027](../decisions/ADR-0027-software-management-providers.md)). Endpoint Agent management is a later/optional phase. Remote access is never a side effect of generic command execution.

## Command requirements

Every privileged command includes command ID, agent/device/tenant identity, capability, issuer/system context, issued/expiry time and correlation/audit context. Duplicate delivery is safe and expired commands never execute.

## Remote access

Remote access is delivered by a Remote Access Provider (HopToDesk, RustDesk, AnyDesk as first providers), not by either agent ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md)). Neither the Connector Agent nor the Endpoint Agent carries remote-desktop, terminal or file-transfer traffic. The provider's endpoint client and relay are a separate third-party trust boundary; the client is version-pinned, signature-verified and installed as ordinary managed software.

Turaco still enforces the session rules: identity of the initiating User, a dedicated permission, MFA/step-up and policy evaluation, user consent by default, unattended access only for Devices named by an explicit unattended-access policy record and only when the provider supports per-session expiring credentials, a visible session indicator, session audit and no reusable or permanent session token. A native provider would need its own ADR and threat model.
