---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-08-02
source: task_turn
source_id: 780f6066ceeb35bc37d2f3f4298ae777:0b30972c2a3aa7d2
confidence: high
title: OpenVibely Update System
---

The OpenVibely update subsystem has a shared signed-update architecture across source, desktop, standalone binary, hosted, Docker-agent, and Docker-manual distributions. The coordinator rollback-failure drain-stranding defect was fixed and broadly validated in commit `ba88dda8` on 2026-08-02. The overall task remains incomplete pending another fresh separate strictly read-only audit.

Durable architecture:
- One immutable build identity supplies version, commit, build time, and artifact across server, desktop, Docker labels, update requests, health responses, release binaries, and macOS bundle metadata. Artifact plus validated container mode determines the distribution; runtime configuration cannot relabel an artifact.
- Every distribution uses the privacy-limited signed daily update-check client. Source builds only check and do not expose installation. Packaged releases require Ed25519-signed canonical metadata, semantic-version and rollback checks, expiry, exact target/platform matching, size/digest verification, redirect isolation, persisted highest accepted version, jitter, and failure backoff. Metadata is revalidated before staging, when apply is requested, and again after draining immediately before desktop or binary installer ownership and apply.
- Durable coordinator and drain state survive restart. Operations own an exact drain generation; duplicate ownership and concurrent apply are rejected, stale operation goroutines cannot mutate a replacement generation, and periodic checks cannot overwrite active transitions.
- Admission closes before the active-work snapshot. Task, Chat, workflow, and Automation entry paths must retain queued input or return an explicit retryable maintenance response. Remote cancellation must succeed before admission reopens, and cancellation is rejected once replacement is non-cancellable.
- Successful drain completion, cancellation, and lease expiry emit a coalesced reopen signal only after durable reset succeeds. The server consumes it to resume worker dispatch and scan both durable queued Chat and task-thread inputs; the same combined recovery runs at startup.
- Desktop updates replace the complete app bundle through the pinned Wails updater adapter and retain a rollback bundle. Standalone binary updates use an external helper, sibling staging/backup paths, restart integration, health/version validation, and rollback. Startup reconciliation must settle successful replacement or old-binary rollback without stranding the drain.
- Hosted mode uses authenticated directive polling, readiness claims, exact generation ownership, durable renewable leases, cancellation, policy/version validation, unsupported-version declines, and restart reconciliation. Hosted staging without a local installer fails before coordinator mutation. Hosted cancellation and lease release reopen admission through the shared drain signal and therefore resume durable queued work.
- Docker-agent mode uses restricted version/readiness APIs without Docker credentials, image names, commands, Compose paths, or socket access. It persists interrupted requests, and success requires reported current and target versions to match the signed desired version. Docker-manual mode only prepares for user-operated restart and exposes the exact OCI digest plus live transition and active-work state. Manual operations remain supervised through lease expiry, including recovery from a persisted `ready` state; expiry clears coordinator state and reopens admission.
- OCI targets require a complete valid image reference, optional valid tag, and exact lowercase `@sha256:<64 hex characters>` digest rather than only a nonempty prefix and valid digest suffix. `/api/system/health` has one authenticated schema for ready and non-ready responses and backs the dependency-free container healthcheck. The Alerts page owns update UI; source builds do not show it.
- Official release builds require a canonical embedded release key ID and 32-byte Ed25519 public key. Configured custom or rotation key files may add trust keys but cannot reuse and replace an embedded key ID. macOS desktop releases require signing, notarization, stapling, and verification for both architectures; packaging fails rather than silently skipping them.
- Update-check disabling is separate from recovery: `DISABLE_UPDATE_CHECKS=true` disables periodic checks but must not disable drain, hosted, or Docker-agent reconciliation.

Earlier lifecycle and persistence fixes completed on 2026-08-02:
- Recovery is deferred until its installer dependencies are ready: desktop recovery waits for Wails updater binding, binary recovery waits for `HealthURL` initialization, and manual Docker recovery resumes without an installer. Deferral does not consume the coordinator's one-time recovery guard.
- Drain ownership, renewal, release, cancellation, expiry, and coordinator apply transitions fail closed when durable persistence fails. Hosted readiness persists a stable idempotency key before its remote claim, cancellation intent is persisted before admission reopens, and desktop rollback surfaces durable release failures.
- Docker-agent request creation and cancellation use persisted idempotency keys. A crash after remote acceptance but before saving the request ID replays the same create request, and the coordinator preserves `applying` plus the owned drain while recovery is pending instead of entering rollback.
- Hosted-managed updates cannot be cancelled through the local coordinator endpoint, and the Alerts UI hides the local Cancel action for Hosted distributions; Hosted administrator directives remain authoritative.

Latest implementation fixes completed on 2026-08-02:
- Desktop and standalone binary installs revalidate release freshness at the final post-drain boundary before ownership and installer apply, preventing metadata that expires during the drain from being installed.
- Docker-manual lease expiry is autonomously reconciled while ready and after restart, clearing durable coordinator state and reopening admission.
- Drain reopen notification resumes worker dispatch and durable queued Chat/task-thread recovery after coordinator completion, Hosted cancellation, Hosted lease release, and expiry.
- Configured key files are rejected when a key ID collides with an embedded official trust key.
- OCI validation now checks the complete registry/repository/tag/digest reference.

Latest Hosted fixes completed on 2026-08-02:
- A `204 No Content` directive poll now reconciles durable nonterminal Hosted operations instead of returning immediately. Restored `waiting_for_idle` drains reach local lease expiry and durably reopen admission; restored `claiming_ready` handoffs replay the persisted readiness idempotency key, resume lease handling, and release admission at the persisted deadline if the remote operation has disappeared.
- Hosted durable assignment state now includes policy and drain-lease metadata in addition to desired version and release metadata. A repeated directive with the same update ID must match the persisted desired version, policy, lease duration, and release metadata or fail closed without issuing readiness.
- The same assignment-consistency check applies both to the outer directive poll and to in-flight polling while active work drains.
- Restored owned `ready` operations enforce persisted `LeaseExpiresAt` before renewal. Expired operations are durably cancelled, their exact drain generation is released, and admission reopens; the renewal loop also checks the deadline before sending a request and before accepting a response so a request crossing expiry cannot revive ownership.
- Same-ID repeated directives are compared with the durable assignment before unsupported policy/version validation can decline them. Conflicting policy, current-version, or older-version directives therefore fail locally without readiness, progress, lease, or decline side effects.

Latest Hosted lease-supervision fix completed on 2026-08-02:
- Commit `928b8be4` fixes `renewUntilReplacement` so unsupported schemas, invalid lease states, renewal failures, and Hosted state-persistence failures remain supervised through the last durably accepted lease deadline instead of returning and relying on directive polling.
- At expiry, durable cancellation and exact drain release are retried until successful; context shutdown preserves fail-safe closed state rather than reopening admission without durable evidence.
- A `replacement_started` response remains non-cancellable and its state transition is retried until durably persisted, preventing an error path from incorrectly reopening admission.
- Regression coverage includes unsupported lease schemas, invalid lease states, failed renewal-state persistence, and a failed first cleanup write that must retry to durable release.

Latest rollback-state and coordinator persistence fixes completed on 2026-08-02:
- Commit `26646f7d` makes `Client.CheckIfDue` fail closed before network access when persisted rollback-protection state is malformed or unreadable; invalid state is not overwritten.
- If persisting the first post-ownership `applying` or `restarting` transition fails, installer execution is blocked and a cleanup supervisor repeatedly releases the exact drain generation and persists terminal coordinator state.
- Cleanup tolerates transient drain-state persistence failures and concurrent cancellation. Durable idle drain state and terminal coordinator state survive restart; an older persisted transition is normalized from the durable idle drain after a crash.
- Regression coverage includes corrupt and unreadable client state, applying and restart-validating transition failures, transient drain-release persistence failures, durable restart recovery, and cancellation during cleanup.

Latest coordinator drain-supervision fixes completed on 2026-08-02:
- Commit `c1a8207c` retries durable `validating` and `rolling_back` transitions before continuing validation or rollback side effects, so transient coordinator persistence failures cannot strand an owned drain after installer apply.
- Asynchronous terminal and abort cleanup now autonomously retries exact-generation drain release and terminal coordinator persistence. Terminal persistence failure retains the operation generation in memory so cleanup remains supervised and restart normalization stays valid.
- Cleanup supervisor ownership is tracked per generation. A failed cancellation retries autonomously only when no existing cleanup supervisor owns that generation, preventing concurrent cleanup paths from overwriting a required `failed` result with `idle`.
- Docker-manual operations retain their waiter when persisting `ready` fails, allowing lease expiry to durably clear the drain and reopen admission without an unrelated status request.
- Regression coverage includes transient `validating` and `rolling_back` writes, successful and abort terminal drain-release retries, terminal coordinator persistence retry, cancellation retry and cleanup races, and autonomous manual-ready expiry.

Latest coordinator rollback-failure cleanup fixes completed on 2026-08-02:
- Commit `ba88dda8` routes runtime rollback errors and desktop startup missing-record or rollback-error paths through exact-generation terminal cleanup instead of persisting `failed` while retaining an owned drain.
- Cleanup durably releases only the persisted owned generation, records terminal `failed`, and retries transient drain-release or coordinator-persistence failures without retrying rollback or replacement side effects.
- Restart recovery recognizes legacy persisted `failed` operations that still retain a matching owned generation and performs cleanup only, preserving the original failure result while reopening admission.
- Regression coverage includes runtime rollback failure, transient cleanup persistence failure, missing desktop rollback records, desktop rollback errors, and restart recovery of legacy stranded `failed` operations.

Current audit status on 2026-08-02:
- The rollback-failure drain-stranding defect found in the audit of commit `c1a8207c` was fixed in commit `ba88dda8` and validated.
- The overall task remains incomplete pending another fresh separate strictly read-only audit from scratch that finds no material bugs, regressions, or missing requirements.

Validation completed after commit `ba88dda8` on 2026-08-02:
- Focused rollback cleanup regressions and the complete `internal/update` package passed.
- `go test -race ./internal/update -count=1 -timeout 120s`, `go build ./...`, and `go vet ./...` passed.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`, `make templ`, and `git diff --check` passed; templ generation produced no updates.
- The worktree was clean after commit `ba88dda8`. The only output caveat was the established non-failing macOS object-version linker warning.
