---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-08-10
source: consolidation
source_id: memory_consolidation_2026_08_10
confidence: high
title: OpenVibely Update System
---

OpenVibely uses one generalized packaged-application update flow across macOS, Windows, and Linux. Standalone updates do not depend on systemd, launchd, Windows Services, or OS installers.

Final shared flow:
- Check the signed release API and select the exact distribution, OS, and architecture artifact.
- Verify the Ed25519 release signature and artifact SHA-256, then stage the complete replacement.
- Require one user approval, durably preserve accepted intent, drain OpenVibely work, and keep admission closed through replacement validation or rollback settlement.
- Launch an independent detached updater helper, shut down OpenVibely, replace and relaunch the installation, and validate the expected version through `/api/system/health`.
- If validation fails, stop the failed replacement, restore the prior installation, and relaunch it.
- Preserve signed catalog handling, update UI, approval, durable coordinator/drain state, health validation, rollback, and source/Hosted/Docker behavior.

Distribution contracts:
- Standalone binary: copy the signed running executable and launch that copy as the updater helper. It waits for the original process to exit, atomically replaces the executable, and relaunches with the original arguments and working directory. Preserve arguments and working directory without putting secrets in command-line arguments or durable update state. Official macOS and Windows standalone artifacts are ZIP archives and Linux artifacts are TAR.GZ archives; after catalog and archive verification, staging accepts exactly one root-level regular `openvibely` or `openvibely.exe` member and publishes only the extracted executable.
- Wails desktop: retain the Wails adapter for signed staging, but use an OpenVibely-owned detached helper for replacement, relaunch, health validation, rollback, and crash recovery. The native install unit is a complete `.app` directory on macOS and the signed desktop executable on Windows/Linux. The helper durably journals exact replacement phases, uses native atomic publication, validates the expected version independently through `/api/system/health`, stops a failed successor before restoring and relaunching the predecessor, and reconciles interrupted phases after restart. Stage/apply/recovery protect app data, database, project root, desktop config, plugin root, and custom trust files against every updater-owned live, backup, staging, temporary, failed-install, Wails `.bak`, helper, journal, atomic `.tmp`, and lease path, including direct and symlink-resolved placement.
- Git source: retain the daily metric-only no-op behavior.
- Hosted and Docker: retain externally controlled container replacement.
- Manual Docker users approve an available update through `POST /api/system/update/apply`, which reaches `Coordinator.Accept`; this manual accepted path may drain active work and transition to `StateReady` without an installer or staged artifact because Docker replacement is externally controlled. Non-manual accepted updates still require a staged artifact before applying.
- Platform-specific code is limited to detached helper creation, waiting for parent exit, atomic replacement, relaunch, and stopping a failed successor before rollback.
- Interrupted replacement must always leave a bootable executable, and startup reconciliation must settle durable state if the helper dies or power is lost.

Obsolete standalone service-manager concepts must be removed directly because this work has not shipped: `OPENVIBELY_UPDATE_RESTART`, `OPENVIBELY_UPDATE_RESTART_TARGET`, restart mode/target state, manager-origin compatibility state, systemd and launchd commands, launchd labels and cleanup, service-manager lifecycle tests, and corresponding CI/docs. Do not add migrations or backward compatibility for those unreleased formats.

Release artifact trust:
- Packaged standalone binary and desktop builds must always perform startup/daily signed release checks for anonymous update metrics; there is no operator setting to disable those checks. Remove `DISABLE_UPDATE_CHECKS` from configuration, code, tests, and documentation.
- `DISABLE_UPDATE_NOTIFICATIONS` is the only update-policy switch: it defaults to `true` for packaged standalone binary and desktop builds, hiding update offers and preventing download, staging, and installation; setting it to `false` enables the packaged update flow. Source metric checks and Hosted/Docker behavior remain unchanged. Disabling notifications does not hide or abandon an already accepted or in-progress update recovery.
- Until macOS and Windows signing credentials are provisioned, packaged binary and desktop notifications/download/staging/installation must default off. Publishing a GitHub release alone must not cause existing installations to update; unsigned desktop artifacts should be omitted, and draft GitHub releases may be used when repository-watcher release notifications are also undesirable.
- Ed25519 catalog verification and SHA-256 artifact verification remain mandatory on every platform; OS signing supplements rather than replaces them.
- macOS Wails bundles must be Developer ID signed with hardened runtime, notarized, and stapled. Raw macOS server binaries must be Developer ID signed and shipped in a notarized archive.
- Windows desktop and server executables must be Authenticode signed and timestamped before packaging. Official release builds and publication fail closed if the required Windows desktop artifact is absent; a native/cross-compiled binary or explicit prebuilt release-job input is required.
- Official releases also require a Linux amd64 desktop tarball, built natively with GTK/WebKit dependencies or supplied from an explicit Linux release-job input. Linux trust continues to rely on signed OpenVibely release metadata and SHA-256 verification.
- A copied updater helper must retain the original executable's OS signature.
- Update-check telemetry includes a client-owned, cryptographically random 128-bit lowercase-hex `install_id` stored only in `AppDataDir/update-state.json`, not the application database. It survives upgrades, is sent only by the update-check client, and rotates every 90 days so it cannot split the hosted service's 30-day active-user window. The hosted service stores only an HMAC and never joins it to accounts or sessions. `OPENVIBELY_DISABLE_INSTALL_ID`, by presence including an empty value, omits the request field and prevents ID generation/storage; never log the raw value. Persistence failure of this optional metric must not block an update check.
- Artifact URL policy must be enforced on every HTTP redirect hop, not only the initial signed release URL. Both `Client.Fetch` and `Client.Download` currently allow an approved HTTPS artifact URL to redirect to prohibited plaintext HTTP or local/private destinations; this is tracked in GitHub issue #210 and needs redirect-policy regression coverage.
- Local implementation direction for GitHub issue `#366`: `Client.CheckIfDue` should record a fresh successful-check timestamp only after packaged signed release verification succeeds. Release verification failures should persist retry backoff without refreshing `LastSuccessfulCheck`, so retries after `NextAttempt` make another HTTP request and a corrected signed release can be accepted and cached normally. Source metric-only checks still persist the 24-hour success throttle after schema validation, and rollback protection through `HighestAcceptedVersion` plus metadata expiration behavior should remain covered by focused `internal/update` regressions.
- Release tooling must expose signing-credential configuration hooks and fail official release validation when required signing has not occurred. Credentials must never be generated or invented.

Required validation includes macOS, Windows, and Linux builds/tests; successful replacement; health/version validation; rollback; invalid signatures; interrupted replacement; source/Hosted/Docker behavior; and release-script validation. The `packaged-update-native` CI matrix runs only macOS and Windows because the aggregate Ubuntu `go test ./...` job already covers `internal/update` on Linux.
