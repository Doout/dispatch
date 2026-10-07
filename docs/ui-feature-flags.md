# Experimental UI

Controller owners can enable individual features under **Settings → Experimental UI**.
All six switches start off, including on upgraded controllers. Changes save immediately
and apply to everyone using that controller. The separate **New interface** preference
still selects the layout for the current account and browser.

These flags hide UI entry points and prevent disabled pages from loading their data or
showing actions. Bookmarked pages explain which setting is disabled. Open pages receive
changes through overview updates and remove the disabled forms. Disabling a flag does
not cancel an accepted operation, delete records, change permissions, or disable API and
CLI access. Existing jobs continue. These are rollout controls for the UI, not access
controls for the backend.

## Audit of recent additions

The initial flags cover resource workflows exposed by the new interface and the workload
backup tab in Services. They already have automated tests. Their remaining checks concern
complete browser workflows or infrastructure that the bundled mock provider cannot create.

| Setting | UI covered | Validation still needed |
| --- | --- | --- |
| Machine provisioning | Machines and provider registration, machine creation, enrollment, deletion, and operation recovery | Allocate, enroll, operate, and delete a real machine through the browser using a real provider adapter. |
| Machine snapshots | Capture, deletion, isolated clone restore, and snapshot or clone recovery actions on Machines | Complete a real snapshot and restore drill, including guest boot, fresh identities, disk independence, and clone isolation. |
| Workload backups | Recovery page and Services backup tab, including capture, verification, restore, and archive deletion | Complete browser mutations against a running database; check recognizable restored data and the final resource inventory. Independent-host and live object-store acceptance remain separate release requirements. |
| Automation credentials | Automation account creation, credential issue, rotation, revocation, and project permissions | Complete the browser lifecycle, use the issued credential, and verify old credentials stop working after rotation or revocation. |
| Project assignments | Adding and removing target, provider, SSH key, and service template assignments | Complete browser changes as an owner and project operator, then verify the affected user sees only the assigned resources. |
| Request receipts | Receipt lookup and linked operation details | Follow a real accepted request through completion and replay, including failed or unavailable operation details. |

Machine provisioning and machine snapshots can be enabled separately. Snapshot restore
includes the form for creating an isolated clone. Provider capability configuration belongs
to Machine provisioning; Machine snapshots controls capture and restore actions.

The existing Operations setting continues to control controller backups and other
Operations tools. Analytics, workloads, run history, and the existing Servers connections
remain available under their current interface settings and permissions. They are not
part of these six resource flags. Target bootstrap and temporary-environment components
without a rendered page do not get unused switches.

The audit used the resource page tests, interface tests, and existing backend suites,
alongside the [live release checklist](paas-release-acceptance.md) and
[machine snapshot evidence limits](machine-snapshots.md). Passing a mocked provider test
does not establish that a real machine booted or a copied disk was sanitized.

## Settings API

`GET /api/v1/settings` returns every flag under `uiFeatures`. The same values appear in
the overview response for navigation. Only controller owners can read or change the
settings endpoint. An omitted flag in an older overview response is treated as disabled.

`PUT /api/v1/settings` changes only supplied values. For example:

```json
{"uiFeatures":{"workloadBackups":true}}
```

This preserves Operations and every other flag, including updates saved by another owner.
Unknown flag names, non-boolean values, nulls, and empty patches are rejected. Existing
callers that send only `operationsEnabled` preserve all UI flags.

Before enabling a feature for routine use, record the browser workflow and any required
provider acceptance evidence. Keep evidence in sanitized text or JSON, without credentials
or screenshots. See the [settings contract](operations.md) for API details.
