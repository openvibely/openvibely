---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-09-15
source: consolidation
source_id: memory_consolidation_2026-09-15
confidence: high
title: OpenVibely Update System
---

OpenVibely uses one generalized packaged-application update flow across macOS, Windows, and Linux. Standalone updates are independent of systemd, launchd, Windows Services, and OS installers.

Shared update contract:
- Check the signed release API and select the exact distribution/OS/architecture artifact. Verify the Ed25519 release signature and artifact SHA-256 before staging a complete replacement.
- Obtain one user approval, durably preserve accepted intent, drain work, and keep admission closed through replacement validation or rollback settlement. A detached independent updater shuts down the app, replaces/relaunches it, and validates the expected version through `/api/system/health`.
- On validation failure, stop the failed successor, restore the previous installation, and relaunch it. Interrupted replacement must leave a bootable executable; startup reconciliation settles durable state after helper death or power loss.
- Packaged local offers are user-initiated, not automatic installation. Actionable releases use the sticky global purple update toast and Alerts `Update` badge; ordinary unread counts remain independent. `/api/system/update` is authoritative, and success/current state clears the card/badge with at most one fingerprint-keyed success toast.
- `view_system_update` mirrors the visible coordinator snapshot for read-only Plan/Orchestrate reporting and returns not-applicable when hidden/absent. Browser surfaces share `window.openVibelyNormalizeSystemUpdateSnapshot`. Drain snapshots preserve `drain.queued_total`.

Distribution and recovery:
- Standalone artifacts are ZIPs on macOS/Windows and TAR.GZ on Linux. Staging accepts exactly one root-level regular `openvibely` or `openvibely.exe` member after catalog/archive verification.
- Standalone, Windows desktop, and Linux desktop use `executable-update-helper`; macOS `.app` uses `app-bundle-update-helper`. Wails remains the signed staging adapter; OpenVibely-owned detached helpers handle replacement, relaunch, health validation, rollback, crash recovery, journals, authorization, leases, and recovery.
- Helpers retain the original executable's OS signature, use atomic journal handoff/publication, native atomic replacement, independent health/version validation, and successor shutdown before rollback. Shared lifecycle assembly is consolidated while platform adapters retain replacement/relaunch details and metadata such as arguments and working directory.
- Stage/apply/recovery protect app data, database, project root, desktop config, plugin root, custom trust files, and all updater/helper/journal/lease temporary paths, including symlink-resolved placement.
- Git source keeps daily metric-only no-op behavior. Hosted and ordinary Docker replacement is externally controlled. Manual Docker users may approve through `POST /api/system/update/apply`; that path may drain to `StateReady` without a local artifact.

Trust and policy:
- Packaged standalone/desktop builds perform startup/daily signed release checks for anonymous update metrics. `DISABLE_UPDATE_NOTIFICATIONS` is the only policy switch and defaults true for packaged builds; false enables offers/download/staging/installation. There is no separate signed-check switch.
- Until macOS/Windows signing credentials exist, packaged offers and installation remain disabled by default; publishing a GitHub release alone must not update installations. Required-signing artifacts are omitted and official validation fails closed when required artifacts are absent.
- Ed25519 catalog and SHA-256 verification are mandatory on every platform. OS signing supplements them: macOS requires Developer ID, hardened runtime, notarization, and stapling; Windows requires Authenticode signing/timestamping; official releases require a Linux amd64 desktop tarball.
- Telemetry uses a client-owned random 128-bit lowercase-hex `install_id` stored only in `AppDataDir/update-state.json`, sent only by update-check, and rotated every 90 days. Hosted storage keeps only an HMAC. `OPENVIBELY_DISABLE_INSTALL_ID`, including an empty value, omits the field and prevents generation/storage; the raw ID is never logged.
- Artifact URL policy applies to every redirect hop. Successful-check timestamps are recorded only after packaged signed verification; failures retain retry backoff. Source metric-only checks retain the 24-hour throttle after schema validation.

Implementation boundaries and gaps:
- Packaged-update integration timeout overrides are centralized in `internal/update.ApplyIntegrationTimeoutOverrides`, which owns lookup, `Atoi`, millisecond conversion, and assignment for both environment variables. Desktop executable/app-bundle adapters, server update dispatch, and fixture branches delegate to it while callers retain their own config loading, helper invocation, and invalid-value return-versus-fatal behavior.
- Existing timeout-override contracts include variable names, unset defaults, relaunch decoding, helper argument parsing, installer handoff, operation-specific defaults, nil-config no-op behavior, and caller-level handling of malformed values.
- Docker managed-update rejection has an open terminal-state gap in `#1081`: definitive agent `400`/`401`/`403` failures must settle visibly as failure and reopen admission instead of leaving drain recovery pending indefinitely.
- Required validation spans supported OS/architectures, replacement, health/version, rollback, invalid signatures, interruption, source/hosted/Docker behavior, and release scripts. The headless desktop E2E switch is test-only.
- The former standalone service-manager restart concepts were unreleased and should stay removed rather than migrated: restart env vars/mode/target state, manager-origin compatibility state, systemd/launchd commands/labels/cleanup, and related tests/docs.
