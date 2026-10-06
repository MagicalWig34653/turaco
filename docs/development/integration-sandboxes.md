# Integration sandboxes and demo access

**Status:** Guide, 2026-10-07. It explains how to get a test or demo counterpart for every external interface Turaco talks to or will talk to, and what Turaco needs from it. Vendor programs, trial lengths and prices change: **check the linked vendor pages before relying on a detail** (marked *verify*). Nothing here is a commitment of Turaco. [Current status](../product/current-status.md) says which integrations exist; most live clients are not built yet and run on fakes or imports.

## Overview

| Interface | Turaco side | Needs a real counterpart? | Easiest way to get one | Effort |
| --- | --- | --- | --- | --- |
| Security advisory feeds (CISA KEV, OSV, NVD, MSRC) | F8 port `integrations/advisories` (Fake + import) | **No account needed** | Public APIs; free NVD key optional | Low — can be built now |
| SMTP (notifications) | Implemented (`SMTP_*`) | No | Mailpit in Docker | Very low |
| LDAP / Active Directory sync, Kerberos SSO | Implemented (F1) | Yes (any LDAP) | Samba AD DC or OpenLDAP in Docker | Low–medium |
| OIDC login (Entra later) | Planned (ADR-0013) | Yes | Keycloak in Docker for development; free Entra tenant for the real thing | Low (Keycloak) |
| Microsoft Intune / Graph (F6) | Port `integrations/intune` (Fake + import) | **Yes**, a tenant with Intune | Trial tenant (see below) | Medium |
| IntuneGet (F9) | Port `integrations/softwaremgmt` (Fake) | Yes (Entra app, Windows packaging) | Docker for the web app; tenant for real use | Medium–high |
| Autotask PSA (F5) | Port + internal sync (Fake) | Yes | Kaseya/Datto trial or partner sandbox | High (no self-service) |
| Remote access (F10): RustDesk, HopToDesk, AnyDesk | Planned, ADR-0026 | Yes, per provider | RustDesk self-hosted; vendors' free/trial plans | Low–medium |
| Microsoft Teams (notifications) | Planned | Yes | Same trial tenant as Intune | Low once a tenant exists |
| S3 object storage | Implemented | No | Adobe S3Mock in the dev compose stack | None |

## Things that need no account

- **CISA Known Exploited Vulnerabilities** is a public JSON feed with no key. **OSV.dev** has a free public API without a key. **NVD API 2.0** is free; a key (requested by e-mail) raises the rate limit. **Microsoft Security Update Guide** data is public. These cover the F8 "live advisory feeds" gap: a real adapter for KEV + OSV (+ NVD) can be built and tested against the live services today. *verify rate limits and terms of use.*
- **SMTP:** run Mailpit (`axllent/mailpit`, ports 1025/8025) and set `SMTP_HOST=localhost`, `SMTP_PORT=1025`, `SMTP_SECURITY=none`, `SMTP_ALLOW_PLAINTEXT=true`, `SMTP_FROM`, `EMAIL_BASE_URL` on the worker (see [local development](local-development.md)).
- **S3:** already part of `make infra-up`.

## Directory and login

- **LDAP / Active Directory:** a Samba Active Directory domain controller in a container (several community images exist) behaves like AD for LDAP, group nesting and Kerberos without a Windows server; OpenLDAP or 389 Directory Server work for the `LDAP_DIRECTORY_TYPE=openldap` style schemas. Put it on a Docker network, create a read-only service account, a few users and nested groups, and point `LDAP_URL`, `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD_FILE`, `LDAP_GROUP_BASE_DN` at it ([LDAP/AD](../integrations/ldap-ad.md)).
- **OIDC / Entra login:** develop against **Keycloak** in Docker (an OIDC provider with users, groups and claims). The real Entra tenant is only needed to verify Entra specifics (group claims overage, conditional access). A free Entra tenant comes with an Azure free account (credit card for identity verification) — *verify*.

## Microsoft Intune and Graph (the hard one)

Turaco's F6 client needs a **tenant with Intune licenses** and an **app registration** (a separate read-only one for sync; a second, write-capable one only for F9 and disabled by default). Planned minimal read permissions (application permissions, admin consent; *to be finalized when the client is built*): managed devices, apps, configuration, service configuration and groups read access (`DeviceManagementManagedDevices.Read.All`, `DeviceManagementApps.Read.All`, `DeviceManagementConfiguration.Read.All`, `DeviceManagementServiceConfig.Read.All`, `Group.Read.All`, `GroupMember.Read.All`).

How to get a tenant as an individual (the [Microsoft 365 Developer Program](https://learn.microsoft.com/en-us/office/developer-program/microsoft-365-developer-program) has been restricted since 2024 to Visual Studio Professional/Enterprise subscribers and some partner programs, and rejects many individual applications):

1. **Microsoft 365 Business Premium trial** (includes Intune and Entra ID P1; typically one month, up to 25 licenses, a credit card for verification, no charge when cancelled before the end) — the most practical route. *verify current terms on the Microsoft 365 business trial page.* Plan the work so that the interesting data (enrolled devices, apps, assignments) exists before the trial ends; cancel in time.
2. **Visual Studio subscription with a developer sandbox** if you or an employer already hold one (Professional/Enterprise standard subscriptions qualify for the Developer Program tenant, 25 E5 licenses, renewing while active).
3. **A partner or employer tenant** with a dedicated, isolated test group (read-only registration only). Preferred if an employer or customer agrees; never test write features in a production tenant.
4. **No tenant:** keep using the Fake and the JSON import (`turaco-admin` import path for devices/management snapshots). This is fully supported and what all tests use.

Devices: you do not need physical hardware for the *read* side only if you can create Windows 11 VMs (enroll in Intune with a test user) — Apple Silicon Macs can run Windows 11 ARM VMs (UTM/Parallels). The management model (artifacts, assignments, groups) can be exercised with **no devices at all** by creating apps, configuration profiles and group assignments in the tenant.

## IntuneGet (software packaging, F9)

IntuneGet is **AGPL-3.0**; run it as a **separate service** and talk to it only over HTTP — never embed or link its code ([ADR-0019](../decisions/ADR-0019-open-source-first.md), [ADR-0027](../decisions/ADR-0027-software-management-providers.md)). Facts verified on 2026-10-07 from its repository: a Next.js web app with SQLite or Supabase storage, Microsoft **Entra sign-in (MSAL)** for users, packaging on a **Windows runner** (GitHub Actions or a local Windows packager) and upload through the Intune Graph API. Its documented HTTP API (`docs/API_REFERENCE.md`) authenticates with a **Microsoft access token as Bearer** (`POST /api/package`, `GET /api/packager/jobs`, `GET /api/intune/apps`, …) and it can send outgoing webhooks. Consequences: the web app can in principle be run isolated in Docker without a Windows VM (SQLite mode, dummy Entra client id; sign-in and every Bearer API call still need a real Entra app registration). A local attempt on 2026-10-07 stopped earlier: the Next.js production build inside the image was killed for lack of memory in a 4 GB Colima VM (the quick start assumes `colima start --cpu 6 --memory 12`); raising the VM memory is the first step of a retry, which is only worthwhile once an Entra app registration exists, and **real packaging needs a Windows machine** (a GitHub Actions Windows runner needs no local VM). How a service like Turaco would call its API with an application identity instead of a signed-in user is **unverified** and decides the connector design ([IntuneGet](../integrations/intuneget.md)).

## Autotask PSA

There is no self-service sandbox: Autotask (Datto/Kaseya) is sold through partners, and API access needs an API user, an integration code and a zone. Options: ask Kaseya/Datto for a **trial or sandbox** (partners and MSP customers can get a sandbox/"preview" environment — *verify*); use an employer's or customer's sandbox; or develop against the **documented REST API with a mock server** generated from the vendor's OpenAPI/Swagger description. Turaco's side is built and tested with the in-memory Fake; the REST client and webhook endpoint are the open work ([Autotask](../integrations/autotask.md)).

## Remote access providers (F10)

- **RustDesk:** open source; the relay/ID servers (`hbbs`, `hbbr`) run in Docker on your own host and clients are free, so a full self-hosted lab is easy. Its API/console features may sit behind the commercial server edition — *verify what the open-source server exposes* before designing the connector.
- **HopToDesk:** free client; check whether an API for session metadata and device mapping exists and under which plan.
- **AnyDesk:** proprietary; API/custom-client and address-book features are tied to paid plans; a trial is available — *verify*.
Each provider needs its own threat model and integration-surface check before a connector is built ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md)).

## Microsoft Teams

Notifications to Teams need a tenant (the same trial tenant as above) and either a workflow/webhook URL or a bot registration. Not planned before the Intune client exists.

## Practical order

1. Build real adapters for the public advisory feeds (no account).
2. Add Mailpit, Keycloak and a Samba AD container as an optional `make infra-up-lab` compose profile for realistic local testing.
3. Try the Business Premium trial for the Intune/Graph client; run IntuneGet's web app against it once an Entra app exists.
4. Ask Kaseya/Datto about an Autotask sandbox in parallel (long lead time).
5. Self-host RustDesk when F10 starts.
