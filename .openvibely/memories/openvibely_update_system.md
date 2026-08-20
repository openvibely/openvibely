---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-08-19
source: consolidation
source_id: memory_consolidation_2026_08_19
confidence: high
title: OpenVibely Update System
---

OpenVibely uses one generalized packaged-application update flow across macOS, Windows, and Linux. Standalone updates do not depend on systemd, launchd, Windows Services, or OS installers.

Shared update flow:
- Check the signed release API and select the exact distribution, OS, and architecture artifact.
- Verify the Ed25519 release signature and artifact SHA-256 before staging a complete replacement.
- Require one user approval, durably preserve accepted intent, drain OpenVibely work, and keep admission closed through replacement validation or rollback settlement.
- Launch an independent detached updater helper, shut down OpenVibely, replace and relaunch the installation, and validate the expected version through `/api/system/health`.
- If validation fails, stop the failed replacement, restore the prior installation, and relaunch it.
- Preserve signed catalog handling, update UI, approval, durable coordinator/drain state, health validation, rollback, and source/Hosted/Docker behavior.
- Packaged local update offers are user-initiated UI, not automatic installation. Actionable releases surface as a sticky global purple update toast plus a separate Alerts nav `Update` badge; the ordinary unread-alert count remains independent.
- `/api/system/update` is the authority for status. After restart/success, `state=succeeded` or `current_version == available` clears the update card/badge and may show one normal auto-dismiss success toast keyed by release fingerprint.
- Browser update surfaces share `window.openVibelyNormalizeSystemUpdateSnapshot(data)` for release/current-version/succeeded hidden-state decisions and apply-supported/Hosted/Docker/manual/staged/available/failed actionability decisions.
- Update drain snapshots must preserve the `/api/system/update` JSON contract, including `drain.queued_total`, while keeping the global pending queued `thread_inputs` count on an indexed sparse path.

Distribution contracts:
- Standalone binary updates copy the signed running executable and launch that copy as the updater helper. The helper waits for the original process to exit, atomically replaces the executable, and relaunches with original arguments and working directory. Do not put secrets in command-line arguments or durable update state.
- Official standalone artifacts are ZIP archives on macOS/Windows and TAR.GZ archives on Linux; staging accepts exactly one root-level regular `openvibely` or `openvibely.exe` member after catalog/archive verification.
- Wails desktop updates retain the Wails adapter for signed staging but use an OpenVibely-owned detached helper for replacement, relaunch, health validation, rollback, and crash recovery.
- Native desktop install units are a complete `.app` directory on macOS and the signed desktop executable on Windows/Linux. The helper journals replacement phases, uses native atomic publication, validates independently through `/api/system/health`, stops failed successors before restoring predecessors, and reconciles interrupted phases after restart.
- Stage/apply/recovery must protect app data, database, project root, desktop config, plugin root, and custom trust files against updater-owned live, backup, staging, temporary, failed-install, Wails `.bak`, helper, journal, atomic `.tmp`, and lease paths, including symlink-resolved placement.
- Git source retains daily metric-only no-op behavior. Hosted and Docker replacement remains externally controlled.
- Manual Docker users may approve an available update through `POST /api/system/update/apply`; that accepted path may drain active work and transition to `StateReady` without an installer/staged artifact because replacement is external. Non-manual accepted updates require a staged artifact before applying.
- Platform-specific code is limited to detached helper creation, waiting for parent exit, atomic replacement, relaunch, and stopping a failed successor before rollback.
- Packaged-update helper handoff assembly should stay consolidated behind a shared private lifecycle helper used by desktop and binary installers while preserving distribution-specific metadata, handoff authorization, recovery readiness, and error context.
- Interrupted replacement must always leave a bootable executable, and startup reconciliation must settle durable state if the helper dies or power is lost.

Obsolete service-manager concepts:
- Standalone service-manager restart concepts were unreleased and should be removed directly rather than migrated: `OPENVIBELY_UPDATE_RESTART`, `OPENVIBELY_UPDATE_RESTART_TARGET`, restart mode/target state, manager-origin compatibility state, systemd/launchd commands, launchd labels and cleanup, service-manager lifecycle tests, and corresponding CI/docs.

Release artifact trust:
- Packaged standalone binary and desktop builds must always perform startup/daily signed release checks for anonymous update metrics; there is no operator setting to disable those checks.
- `DISABLE_UPDATE_NOTIFICATIONS` is the only update-policy switch. It defaults to `true` for packaged standalone binary and desktop builds, hiding update offers and preventing download, staging, and installation; setting it to `false` enables packaged updates.
- Update-default derivation across config load/normalization/validation should be centralized while preserving mode-specific behavior.
- Until macOS and Windows signing credentials are provisioned, packaged binary and desktop notifications/download/staging/installation must default off. Publishing a GitHub release alone must not cause existing installations to update; unsigned desktop artifacts should be omitted, and draft GitHub releases may be used when repository-watcher notifications are undesirable.
- Ed25519 catalog verification and SHA-256 artifact verification remain mandatory on every platform; OS signing supplements rather than replaces them.
- macOS Wails bundles must be Developer ID signed with hardened runtime, notarized, and stapled. Raw macOS server binaries must be Developer ID signed and shipped in a notarized archive.
- Windows desktop and server executables must be Authenticode signed and timestamped before packaging. Official release builds/publication fail closed if required Windows desktop artifacts are absent.
- Official releases require a Linux amd64 desktop tarball, built natively with GTK/WebKit dependencies or supplied from explicit Linux release-job input. Linux trust continues to rely on signed OpenVibely release metadata and SHA-256 verification.
- A copied updater helper must retain the original executable's OS signature.
- Update-check telemetry includes a client-owned cryptographically random 128-bit lowercase-hex `install_id` stored only in `AppDataDir/update-state.json`, sent only by the update-check client, and rotated every 90 days. The hosted service stores only an HMAC and never joins it to accounts/sessions. `OPENVIBELY_DISABLE_INSTALL_ID`, by presence including empty value, omits the request field and prevents ID generation/storage; never log the raw value.
- Artifact URL policy must be enforced on every HTTP redirect hop, not only the initial signed release URL; redirect-policy regression coverage is tracked in issue `#210`.
- `Client.CheckIfDue` should record a fresh successful-check timestamp only after packaged signed release verification succeeds. Verification failures should persist retry backoff without refreshing `LastSuccessfulCheck`; source metric-only checks still persist the 24-hour success throttle after schema validation.
- Release tooling must expose signing-credential configuration hooks and fail official release validation when required signing has not occurred. Credentials must never be generated or invented.
- Required validation includes macOS, Windows, and Linux builds/tests; successful replacement; health/version validation; rollback; invalid signatures; interrupted replacement; source/Hosted/Docker behavior; and release-script validation. The `packaged-update` CI job runs native update tests plus binary and desktop packaged E2E checks across OS/arch rows, with Windows ARM experimental.
