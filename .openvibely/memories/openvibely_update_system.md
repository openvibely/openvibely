---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: OpenVibely Update System
---

OpenVibely uses one generalized packaged-application update flow across macOS, Windows, and Linux. Standalone updates do not depend on systemd, launchd, Windows Services, or OS installers; hosted and ordinary Docker replacement is externally controlled.

- Verify the signed release catalog (Ed25519) and artifact SHA-256 before staging a complete, exact-distribution replacement. Archive extraction accepts only the expected root-level regular executable. OS signing supplements catalog verification: macOS Developer ID/notarization, Windows Authenticode, and required official artifacts are fail-closed release requirements.
- Packaged updates are user-initiated. One approval is durably recorded, work drains, and admission remains closed through validation or rollback. Detached platform helpers replace/relaunch and validate the expected version through `/api/system/health`; failure restores and relaunches the previous installation. Startup reconciliation settles interrupted operations.
- Protect app data, database, project root, desktop configuration, plugins, custom trust files, and updater temporary state, including symlink-resolved paths. Helpers use atomic journals/replacement and OS-level authorization where required.
- `/api/system/update` is authoritative for visible state. Actionable offers use the sticky global update toast and Alerts badge; ordinary unread counts remain independent. Success/current state clears the offer with at most one fingerprint-keyed success toast. `view_system_update` is read-only and reflects the visible coordinator state.
- Packaged standalone/desktop builds perform signed release checks for anonymous update metrics. `DISABLE_UPDATE_NOTIFICATIONS` is the single policy switch and defaults to disabled offers/installations in packaged builds until signing credentials are available. A GitHub release alone must not enable installation.
- Telemetry uses a client-owned random install ID stored only in update state and sent only with update checks; hosted storage retains only an HMAC. The explicit disable variable suppresses generation, storage, and transmission. Never log raw IDs or credentials.
- Docker users may approve a managed apply request, but the server has no local artifact to replace. A known gap is definitive managed-update rejection leaving drain recovery unsettled; such failures should visibly settle and reopen admission.
- Required validation covers supported platforms, replacement, health/version checks, rollback, invalid signatures, interruption, and recovery. Do not reintroduce unreleased service-manager restart concepts without an explicit request.
