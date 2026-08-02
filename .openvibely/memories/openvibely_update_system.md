---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-08-02
source: consolidation
source_id: memory_consolidation_2026_08_02
confidence: high
title: OpenVibely Update System
---

The OpenVibely update subsystem is governed by `/Users/dubee/go/src/github.com/runbooks/openvibely-update-implementation.md`. The implementation exists, but the latest required read-only audit found material defects, so it is not cleanly certified and the task remains active.

Durable architecture:
- One immutable build identity supplies version, commit, build time, and artifact across server, desktop, Docker labels, update requests, health responses, release binaries, and macOS bundle metadata. Artifact plus validated container mode determines the distribution; runtime configuration cannot relabel an artifact.
- Every distribution uses the signed daily update-check client. Source builds only perform the privacy-limited check and do not expose update state or installation. Packaged releases require Ed25519-signed canonical metadata, semantic-version and rollback checks, expiry, exact target/platform matching, size/digest verification, redirect isolation, persisted highest accepted version, jitter, and failure backoff. Metadata is revalidated before staging and apply.
- Durable coordinator and drain state survive restart. Admission closes before the active-work snapshot, and task, chat, workflow, and Automation paths must retain queued input or return an explicit retryable maintenance response. Cancellation and unowned lease expiry reopen admission.
- Desktop updates replace the complete app bundle through the pinned Wails updater adapter and retain a full rollback bundle. Standalone binary updates use an external helper, sibling staging/backup paths, restart integration, health/version validation, and rollback.
- Hosted mode uses authenticated directive polling, readiness claims, generation ownership, durable leases, renewal, and cancellation. Docker-agent mode uses restricted version/readiness APIs without Docker credentials, image names, commands, Compose paths, or socket access. Docker-manual mode only prepares for a user-operated restart.
- `/api/system/health` has one authenticated schema for ready and non-ready responses and backs the dependency-free container healthcheck. The Alerts page owns update UI; source builds do not show it.

Current material blockers from the latest audit:
- Standalone updates lack the shutdown callback needed for the helper to replace the running parent; rollback startup can also strand `restarting` state and its drain.
- The periodic checker can overwrite active transition states. Apply and cancel are not ownership-safe under concurrency, and cancellation can reopen admission after remote cancellation failure or replacement start.
- Hosted replacement startup does not reconcile desired version or release its drain after success; policy and unsupported-version declines are incomplete.
- Docker-agent completion does not validate reported target/current version, and interrupted persisted requests are not resumed or reconciled.
- Release tooling permits empty trust roots and packages macOS desktop updates without required signing/notarization. OCI digest validation is weak, and Docker-manual UI omits exact digest and live transition/activity refresh.

These defects require a separate implementation turn followed by a fresh from-scratch read-only audit with no material findings.
