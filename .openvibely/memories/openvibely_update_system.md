---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: OpenVibely Update System
---

OpenVibely uses one generalized packaged-application update flow across macOS, Windows, and Linux. Standalone updates are independent of systemd, launchd, Windows Services, and OS installers.

Shared update contract:
- Check the signed release API and select the exact distribution, OS, and architecture artifact. Verify the Ed25519 release signature and artifact SHA-256 before staging a complete replacement.
- Obtain one user approval, durably preserve accepted intent, drain work, and keep admission closed through replacement validation or rollback settlement. A detached independent updater shuts down the app, replaces/relaunches it, and validates the expected version through `/api/system/health`.
- On validation failure, stop the failed successor, restore the previous installation, and relaunch it. Interrupted replacement must leave a bootable executable and startup reconciliation must settle durable state after helper death or power loss.
- Packaged local update offers are user-initiated, not automatic installation. Actionable releases show a sticky global purple update toast and an Alerts `Update` badge; ordinary unread-alert counts remain independent. `/api/system/update` is authoritative, and succeeded/current-version state clears the card/badge with at most one fingerprint-keyed success toast.
- `view_system_update` mirrors the visible coordinator snapshot for read-only Plan/Orchestrate reporting and returns not-applicable when update status is hidden/absent. Browser surfaces share `window.openVibelyNormalizeSystemUpdateSnapshot` for state/actionability decisions. Drain snapshots preserve `drain.queued_total` while global queued `thread_inputs` counting stays on an indexed sparse path.

Distribution and recovery:
- Standalone artifacts are ZIPs on macOS/Windows and TAR.GZ on Linux. Staging accepts exactly one root-level regular `openvibely` or `openvibely.exe` member after catalog/archive verification.
- Standalone, Windows desktop, and Linux desktop use `executable-update-helper`; macOS `.app` uses `app-bundle-update-helper`. Wails remains the signed staging adapter, while OpenVibely-owned detached helpers handle replacement, relaunch, health validation, rollback, crash recovery, journals, authorization, leases, and recovery.
- The copied helper retains the original executable's OS signature. Helper handoff and coordinator cancellation are one atomic-winner transition under a shared lease; a prepared private journal is atomically renamed to the active journal before the coordinator can observe it.
- Native install units are a complete macOS `.app` or the signed Windows/Linux desktop executable. Helpers journal phases, use native atomic publication, independently validate health/version, stop failed successors before rollback, and reconcile interrupted phases after restart.
- Stage/apply/recovery protect app data, database, project root, desktop config, plugin root, custom trust files, and all updater live/backup/staging/temp/failed-install/Wails backup/helper/journal/atomic-temp/lease paths, including symlink-resolved placement.
- Platform-specific code is limited to helper creation, waiting for parent exit, atomic replacement, relaunch, and stopping a failed successor. Shared helper lifecycle assembly should remain consolidated while preserving distribution-specific metadata and error context.
- Git source keeps daily metric-only no-op behavior. Hosted and ordinary Docker replacement is externally controlled. Manual Docker users may approve through `POST /api/system/update/apply`; that path may drain and reach `StateReady` without a local artifact, while other accepted updates require staging.

Trust and policy:
- Packaged standalone and desktop builds always perform startup/daily signed release checks for anonymous update metrics. `DISABLE_UPDATE_NOTIFICATIONS` is the only policy switch and defaults true for packaged builds; false enables offers/download/staging/installation. There is no separate switch for the signed check itself.
- Until macOS and Windows signing credentials exist, packaged notifications/download/staging/installation remain off by default; publishing a GitHub release alone must not update existing installations. Unsigned desktop artifacts are omitted when required signing is unavailable.
- Ed25519 catalog and SHA-256 artifact verification are mandatory on every platform; OS signing supplements them. macOS requires Developer ID, hardened runtime, notarization, and stapling for Wails bundles and signed/notarized archives for raw server binaries. Windows executables require Authenticode signing and timestamping. Official releases require a Linux amd64 desktop tarball and fail closed when required Windows desktop artifacts are absent.
- Update telemetry uses a client-owned random 128-bit lowercase-hex `install_id` stored only in `AppDataDir/update-state.json`, sent only by the update-check client, and rotated every 90 days. Hosted storage keeps only an HMAC and never joins it to accounts/sessions. `OPENVIBELY_DISABLE_INSTALL_ID`, including an empty value, omits the field and prevents generation/storage; the raw ID is never logged.
- Artifact URL policy applies to every HTTP redirect hop; redirect coverage is tracked in `#210`. Successful-check timestamps are recorded only after packaged signed verification; failures retain retry backoff. Source metric-only checks retain the 24-hour throttle after schema validation.
- Release tooling must expose signing-credential hooks and fail official validation when required signing has not occurred; credentials must never be invented. Required validation spans all supported OS/architectures, replacement, health/version, rollback, invalid signatures, interruption, source/Hosted/Docker behavior, and release scripts. Windows/Linux desktop packaged E2E may use the test-only `OPENVIBELY_UPDATE_E2E_HEADLESS_DESKTOP=1` switch with the real backend/helper path; it is not a production mode.

The former standalone service-manager restart concepts were unreleased and should stay removed rather than migrated: restart env vars/mode/target state, manager-origin compatibility state, systemd/launchd commands/labels/cleanup, and related tests/docs.
