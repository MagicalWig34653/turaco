# Intune Assignment Intelligence

**Status:** Partly implemented. F6 slice 2 ingests the normalized model; slice 3 adds the evaluator, Assignment Paths and the Device, Directory Group, User and reverse-lookup read views (backend, local data only, see [F6 design](../product/f6-endpoint-intelligence-design.md#slice-3-status)). Memberships that were never synced, cut lookups and exclusions across User and Device groups evaluate to `unknown`, not guessed. Slice 4 (backend, see [Slice 4 status](../product/f6-endpoint-intelligence-design.md#slice-4-status)) adds the artifact and Device history, the Device vs Device and Directory Group vs Directory Group diff, the Turaco-derived `assignment_ineffective` finding and the device list filters. The frontend, `assignment_stale`, expected-applicability history and Saved Views are not implemented.

Turaco should turn Intune's distributed assignment/configuration information into an explainable operational view for **Directory Groups, Users and Devices**. The goal is not to mirror the Intune portal. The goal is to answer quickly:

> What is assigned here, should it apply, what did Intune observe, and why?

## Product principles

1. **Assignment is not application.** A configured assignment does not prove that a Device received or successfully applied it.
2. **Expected applicability is an evaluation, not a claim of Intune internals.** Turaco may calculate expected applicability from known group membership, assignment targets, exclusions and filters, but must label uncertainty.
3. **Observed state remains provider evidence.** Intune-reported install/configuration/compliance state is shown separately with source and freshness.
4. **Every effective result should be explainable.** A User should be able to open "Why?" and see the assignment path and exclusions/filters that affected the result.
5. **Every count is explorable.** A number such as "3,621 expected devices" must drill down to those devices and the evaluation basis.
6. **The normalized model is provider-agnostic.** Intune-specific fields remain available at the adapter/detail boundary, while shared Turaco views use canonical management concepts.

## Canonical three-state view

Turaco must keep these dimensions separate:

| Dimension | Meaning | Example |
|---|---|---|
| **Assigned** | A provider assignment exists to a target such as Group, All Devices or All Users. | `GRP-Windows-Corporate` is included. |
| **Expected Applicable** | Turaco's current evaluation says the assignment should apply to the User/Device based on known membership/filter/provider rules. | Device is in the included group and matches the filter. |
| **Observed** | Intune reports an actual target status/result. | Applied, installed, error, conflict, pending. |

Example:

```text
BitLocker Baseline

Assigned              yes
Expected applicable   yes (high confidence)
Observed               error
Observed at            2026-09-30 14:31
Last provider sync     2026-09-30 14:42
```

This immediately distinguishes a targeting problem from an application problem.

## Normalized management model

### ManagementArtifact

A provider-managed object that can be assigned/evaluated.

Canonical kinds initially include:

- `application`
- `configuration_profile`
- `compliance_policy`
- `endpoint_security_policy`
- `script`
- `remediation`

Fields/concepts include:

- internal ID;
- Management Provider;
- provider external ID;
- artifact kind;
- name/display metadata;
- platform/scope metadata where relevant;
- provider-specific detail payload/reference where necessary;
- source, observed time and last successful sync.

An Intune application artifact may link to a canonical `SoftwareProduct`. That link does not make the provider object itself authoritative for software identity.

### ManagementAssignment

Represents provider intent that a `ManagementArtifact` targets a management scope.

Concepts include:

- artifact ID;
- target kind (`group`, `all_devices`, `all_users`, provider-specific target where supported);
- target external/canonical reference;
- assignment mode (`include` / `exclude` where applicable);
- application intent (`required`, `available`, `uninstall`, etc.) where relevant;
- optional Management Filter and filter mode;
- provider external assignment ID;
- source/freshness;
- active validity/revision history.

### ManagementFilter

Normalized representation of a provider assignment filter.

Stores enough information to:

- identify the provider filter;
- show its current rule/summary;
- record include/exclude mode on an assignment;
- evaluate locally only when Turaco has a supported, deterministic evaluator;
- otherwise mark filter evaluation as provider/unknown rather than guessing.

### ManagementApplicability

A derived/read model for one artifact-assignment path against a User/Device.

Suggested result values:

- `applicable`
- `excluded`
- `not_applicable`
- `unknown`

It records:

- evaluated entity;
- assignment/revision;
- evaluation result;
- confidence;
- explanation/reason codes;
- evaluated at;
- input freshness.

This is **not** authoritative provider state and must be recomputable.

### ManagementObservation

Provider-reported result for an artifact/target.

Examples include:

- app installed / pending / failed;
- configuration applied / conflict / error;
- compliance compliant / noncompliant / unknown;
- endpoint-security policy status.

Every observation retains source, provider IDs and observed time. Provider status values may be preserved alongside a normalized category.

### AssignmentPath

`AssignmentPath` is an explainability/read model, not a new source-of-truth entity.

Example:

```text
CLIENT-01842
  -> member of GRP-Windows-Corporate
  -> included by BitLocker Baseline
  -> filter Corporate Windows Devices
  -> filter result MATCH
  -> expected applicable YES
```

Or:

```text
CLIENT-01842
  -> member of GRP-All-Windows
  -> included by 7-Zip
  -> member of GRP-No-ThirdParty-Software
  -> excluded by 7-Zip
  -> expected applicable NO
```

The path must identify whether membership originated from Device or User targeting when that distinction matters.

## Group view

A Directory Group page should include an **Intune Assignments** area grouped by artifact kind. Intune/Entra Directory Groups are not Turaco operational Teams:

```text
Apps
Configuration
Compliance
Endpoint Security
Scripts / Remediations
```

For each assignment show at minimum:

- artifact name/type;
- include/exclude;
- app intent where applicable;
- assignment filter and mode;
- source/provider;
- last sync;
- expected/effective Device count when computable;
- observed problem counts where provider data supports them.

The Directory Group page should support drill-down from counts to Devices/Users. Group membership used for evaluation retains provider source/freshness; observed membership is preferred over attempting to reproduce Entra dynamic-group rules.

## Device view

A Device should have a management/Intune view containing:

- overview;
- applications;
- configuration profiles;
- compliance;
- endpoint security;
- scripts/remediations where data is available;
- assignment paths;
- assignment history;
- source/freshness.

Each item should show the three dimensions: Assigned, Expected Applicable and Observed.

A **Why is this assigned?** action opens the Assignment Path with included groups, excluded groups, All Users/All Devices targeting, filters and User-vs-Device source.

## User view

A User view should expose management artifacts assigned through User targeting, including:

- direct/provider-supported user scope;
- User Group membership;
- All Users;
- exclusions and filters;
- related managed Devices where useful.

Turaco must not assume a User-targeted assignment has identical results on every Device. Provider observation remains Device/User-context specific.

## Artifact reverse lookup

Every Management Artifact can answer:

> Where is this assigned and what does it currently affect?

Example:

```text
Windows Security Baseline

Include
  GRP-All-Windows                 3,812 known devices
  GRP-Privileged-Workstations       28 known devices

Exclude
  GRP-Windows-Kiosks                45 known devices

Filter
  Corporate Windows Devices

Expected applicable
  3,621 devices
```

All counts must drill down to the underlying set and evaluation reason.

## Compare / diff

### Device vs Device

Turaco should compare effective management context and highlight differences:

```text
                         CLIENT-01842    CLIENT-01987
BitLocker Baseline       applicable      applicable
OneDrive KFM             applied         applied
Defender ASR             applied         excluded
SAP GUI                  installed       not applicable
```

Opening a difference shows the two Assignment Paths and observations.

> Implemented (slice 4): `GET /devices/{id}/management/diff?otherDeviceId=` and `GET /directory-groups/{id}/management/diff?otherGroupId=`. Classes are `same|different|only_left|only_right|unknown`; an unknown input (Assigned, Expected or Observed) stays unknown (`uncertain`) and an item with nothing known to differ but something unknown is class `unknown`, never `same`. The group diff compares configuration only (mode, intent, filter); "differing expected target sets" are not computed.

### Directory Group vs Directory Group

Compare management artifacts and assignment properties between two Directory Groups:

- only in Directory Group A;
- only in Directory Group B;
- same artifact but different intent;
- different filter/mode;
- differing expected target sets.

This supports configuration-drift troubleshooting without pretending Groups themselves are provider policy containers.

## History and timeline

Turaco should retain enough normalized history to explain meaningful changes such as:

- assignment added/removed;
- include/exclude target changed;
- filter changed;
- group membership changed when relevant to an investigation;
- expected applicability changed;
- observed status changed materially;
- source data became stale.

Example:

```text
2026-09-29 09:14  BitLocker assigned through GRP-Windows-Corporate
2026-09-29 16:42  Device became member of GRP-Exceptions-BitLocker
2026-09-29 16:49  Expected applicability: applicable -> excluded
```

Do not store every identical polling result as business history. Preserve meaningful change plus observation freshness.

> Implemented (slice 4): assignment added/changed/removed (target, mode, intent, filter), artifact removed, observation state changes and group membership joins/leaves, derived from the interval and change tables without any per-sync record (`GET /management-artifacts/{id}/history`, `GET /devices/{id}/management-history`). Not stored: expected-applicability changes (it is computed on demand, so only its inputs have history) and "source data became stale".

## Operational findings

Turaco may surface findings/warnings where evidence is sufficient:

- stale Device check-in;
- assignment targets an empty known Group;
- expected applicable but provider observation is missing/stale;
- provider reports configuration conflict/error;
- contradictory include/exclude paths requiring explanation;
- assignment/filter cannot currently be evaluated (`unknown`, not guessed);
- large unexpected drift between similar Directory Groups/Devices.

Warnings must distinguish **provider-reported** problems from **Turaco-derived** findings.

> Implemented (slice 4): `provider_reported_error` (provider-reported, slice 2) and `assignment_ineffective` (Turaco-derived: assigned and expected applicable with confidence high or medium but observed absent or `not_applicable` for more than 7 days, counted from the newest of the assignment, the Device, its group membership and the retirement or state change of the observation). Both kinds need management access to be listed, filtered or shown on the Device. The API and UI keep them as separate kinds; the first is the provider's statement, the second is Turaco's inference from local data and shows no provider text. Stale check-in, empty known groups, include/exclude contradictions and `assignment_stale` are not implemented.

## Query and Saved Views

Management fields should become canonical query dimensions where safe, for example:

```text
Devices where:
  ManagementArtifact("BitLocker Baseline").expected_applicable = true
  AND ManagementArtifact("BitLocker Baseline").observed_status = error
```

Or:

```text
Devices where:
  Intune.last_check_in < now - 7 days
```

> Implemented (slice 4): device list filters `managementState` (`failed|conflict|pending`), `hasFinding`, `osVersion` prefix and `lastCheckinOlderThanDays`. Per-artifact query terms such as `ManagementArtifact("BitLocker Baseline").expected_applicable` are not implemented. Device list filters can be saved as platform Saved Views through the Device field catalog ([ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md)); the per-artifact terms above are not catalog fields.

This query model can power Saved Views, Dynamic Groups, reporting and Security/IT Briefing correlation without allowing arbitrary SQL.

## Synchronization and provenance

The Intune adapter maps Microsoft Graph resources into canonical management objects. Vendor DTOs remain in the integration boundary.

Every synchronized record must retain as applicable:

- provider/external ID;
- source;
- observed time;
- last successful sync;
- source revision/etag/hash where useful;
- deletion/tombstone semantics;
- raw provider status values where normalization could lose troubleshooting detail.

Large syncs run asynchronously and idempotently. Interactive Device/Group views read local normalized state and must not block on live Graph calls by default.

## Authorization

At minimum the design distinguishes:

- viewing normalized management/assignment data;
- administering the Intune Integration/credentials/sync configuration;
- executing future management/deployment actions.

Viewing an Assignment Path must not bypass the caller's authorization to the referenced User, Device, Group or sensitive provider detail.

## Events

Meaningful normalized changes may emit events such as:

- `ManagementAssignmentChanged`
- `ManagementApplicabilityChanged`

High-volume provider polling must not produce unbounded event noise. Events represent meaningful changed facts, not every synchronization read.

## Explicit non-goals for the first implementation

- perfectly reproducing every undocumented Intune internal precedence rule;
- live Graph calls for every page render;
- treating Turaco's expected applicability as authoritative Intune execution state;
- duplicating Entra/Intune as a second general directory/MDM configuration editor;
- hiding provider-specific detail when it is necessary for troubleshooting.

The first successful version should make Intune targeting **understandable and explainable**, not attempt to replace the Intune admin center.

This model is the foundation for planned software rollout orchestration ([ADR-0027](../decisions/ADR-0027-software-management-providers.md)), which adds Turaco's Desired State and Deployment Rings as a separate dimension in front of Assigned / Expected Applicable / Observed.
