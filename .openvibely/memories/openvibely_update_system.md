---
name: openvibely_update_system
type: project
created: 2026-08-02
updated: 2026-08-02
source: task_turn
source_id: 780f6066ceeb35bc37d2f3f4298ae777:df555f2581128a2c
confidence: high
title: OpenVibely Update System
---

The OpenVibely update subsystem has a shared signed-update architecture across source, desktop, standalone binary, hosted, Docker-agent, and Docker-manual distributions. Commit `b9b7971e` fixed the standalone binary replacement crash window, commit `f8db3c44` moved Windows replacement into a sibling helper image, commit `c53e83f6` fixed Windows parent-exit detection, commit `17a27925` fixed failed-successor shutdown before rollback, commit `db4a98f2` moved the systemd update helper into an independently owned transient service, commit `08c7055f` fixed post-handoff systemd helper failure recovery, commit `34b1e309` moved the launchd helper into an independently owned submitted job, commits `f44375b3` through `3749d0fc` hardened helper handoff and authorization, commit `ce38c11f` added durable post-authorization phases and manager-owned recovery, and commit `195b56d8` made dead-helper lease transfer atomic. Commit `92e3292a` disables direct-`exec` automatic binary replacement because neither an ordinary child nor a sibling supervisor provides an independently managed top-level recovery actor. Automatic standalone binary updates now require systemd or launchd. Implementation and validation are complete, and a fresh separate strictly read-only audit of `195b56d8` found no material bugs, regressions, or missing runbook requirements; the workspace remained unchanged.

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
- A `204 No Content` directive poll reconciles durable nonterminal Hosted operations instead of returning immediately. Restored `waiting_for_idle` drains reach local lease expiry and durably reopen admission; restored `claiming_ready` handoffs replay the persisted readiness idempotency key until a definitive response, then resume authoritative lease handling without using the older pre-claim deadline as evidence of rejection.
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

Latest restart-durability fixes completed on 2026-08-02:
- Commit `5109589a` adds a lifecycle-scoped autonomous drain expiry supervisor. It reconciles persisted unowned drains after restart, retries durable reset failures, and no longer depends on unrelated `Status` or `Admit` calls to reopen admission.
- Server startup activates the drain expiry supervisor before worker and update recovery startup.
- Hosted restart recovery replays persisted owned `claiming_ready` operations with the same durable readiness idempotency key. Ambiguous transport, decoding, or schema responses retain exact drain ownership and admission remains closed until the control plane returns a definitive rejection or an accepted authoritative lease.
- The same autonomous drain supervisor cleans up the initial Hosted-assignment crash boundary where the drain was persisted but no Hosted assignment state file exists.
- Deterministic regressions cover a coordinator crash after drain persistence but before `waiting_for_idle`, Hosted restart with no assignment state, same-key replay of restored owned `claiming_ready` state after transient control-plane failure, and autonomous cleanup of unowned drains after restart.

Latest ambiguous external-control-plane fixes completed on 2026-08-02:
- Commit `69ea77bc` classifies Docker-agent create transport/response ambiguity and status transport/response ambiguity as `ErrUpdateRecoveryPending`, preserving durable request and idempotency state instead of triggering rollback cleanup.
- The coordinator retries resumable Docker-agent requests in-process with backoff, as well as after restart, while retaining `applying` and exact-generation drain ownership. It rechecks generation/state after each resume response so concurrent cancellation cannot start a stale rollback.
- Commit `bc9283a2` fixes Hosted readiness ambiguity by repeatedly replaying the durably persisted readiness claim with the same idempotency key while exact drain ownership and closed admission are retained. Transport loss, malformed decoding, and unsupported response schemas are ambiguous and remain replayable; only an explicit `accepted:false` rejection or a valid accepted authoritative lease advances the state machine.
- Once readiness is accepted, the authoritative `lease_expires_at` is persisted and governs renewal or cancellation. The older pre-claim local deadline is never used to reopen admission after an ambiguous readiness response, including after restart or after that local deadline has elapsed.
- Context shutdown during ambiguous readiness reconciliation preserves fail-closed drain ownership rather than reopening admission.
- Regression coverage includes response loss followed by malformed response and eventual acceptance, same-key replay, a later authoritative lease, prolonged control-plane unavailability beyond the pre-claim deadline, explicit rejection, and restart replay.

Latest ambiguous Hosted lease-renewal fix completed on 2026-08-02:
- Commit `d165cb4f` durably persists a stable renewal idempotency key before sending each Hosted lease request. Transport loss, malformed responses, unsupported schemas, and invalid states replay the same key while exact drain ownership and closed admission are retained.
- A definitive valid renewal adopts the authoritative later lease and durably clears the pending key. Definitive cancellation and `replacement_started` responses remain durably terminal.
- Restart recovery immediately replays a persisted pending renewal key even when the previously known lease deadline has passed, rather than treating that old deadline as proof the remote renewal was rejected.
- Regression coverage includes accepted-response loss, malformed replay responses, stable-key reuse, authoritative lease adoption, prolonged fail-closed ambiguity, and restart replay after the previous deadline.

Latest Docker-agent definitive version-mismatch fix completed on 2026-08-02:
- Commit `47356127` classifies a Docker-agent `succeeded` response whose reported current or target version differs from the signed desired version as `ErrUpdateRecoveryPending` rather than an ordinary installer error.
- The coordinator therefore retains durable `applying` state and exact drain ownership, repeatedly reconciles the same external request, and does not invoke unsupported local Docker rollback or reopen admission after an unauthorized replacement identity is reported.
- Durable mismatch state remains available across resume/restart reconciliation. Regression coverage verifies persisted reported identities, recovery-pending classification, exact drain ownership during coordinator reconciliation, and release only after a later definitive agent cancellation.

Latest Hosted exact rollback-identity fix completed on 2026-08-02:
- Commit `fd09baa1` durably records the exact pre-update workspace version when a Hosted assignment is accepted, before readiness or replacement can progress.
- Hosted restart recovery now treats rollback as successful only when the running version exactly equals that durable predecessor identity. An unexpected lower version, such as `0.4.0` during a `0.5.0` to `0.6.0` update, remains fail-closed in `restarting` with the owned drain retained.
- Legacy `restarting` records without a durable predecessor identity also remain fail-closed rather than inferring rollback success. Restart at the exact desired target remains successful through the existing target-version path.
- Regression coverage verifies durable predecessor persistence through the real directive path, exact `0.5.0` rollback success, and rejection of both unexpected lower and higher replacement versions.

Latest Docker-agent rollback-recovery fix completed on 2026-08-02:
- Commit `98ca627f` makes `StartRecovery` handle a persisted Docker-agent `rolling_back` operation with exact drain ownership.
- Because Docker-agent rollback is unsupported and a crash cannot prove whether an external rollback side effect began, recovery does not replay rollback or replacement. It preserves the original failure reason and runs retrying exact-generation terminal cleanup until the drain is durably idle and coordinator state is `failed`.
- Regression coverage performs a true reload of both persisted drain and coordinator state and injects a transient first drain-reset failure, proving autonomous cleanup supervision and durable admission reopening.

Latest desktop crash-safe rollback-backup fix completed on 2026-08-02:
- Commit `77750c67` makes Wails desktop rollback backups crash-safe. The live app bundle is copied and synced into a sibling `.partial` directory, then atomically renamed to the final backup path; an existing committed backup is moved to a sibling `.stale` path while the replacement backup is prepared.
- The final backup path therefore identifies only a fully copied, durably published bundle. Successor rollback ignores `.partial` and `.stale` crash residue, so interruption during backup creation cannot replace a healthy running app with an incomplete bundle.
- Regression coverage forces backup-copy interruption after an entry is copied, verifies no uncommitted final backup is published, simulates leftover `.partial` and `.stale` artifacts, and proves rollback rejects them while preserving the healthy current bundle. Existing complete-bundle and symlink restoration behavior remains covered.

Latest standalone binary crash-safe replacement fixes completed on 2026-08-02:
- Commit `b9b7971e` removes the missing-executable crash window from `RunBinaryHelper`. The helper copies the live executable into a sibling `.partial` backup, explicitly preserves permissions, syncs it, atomically publishes the committed backup, syncs staged content, and atomically replaces the configured executable path.
- Forward replacement and health-validation rollback use platform-native replace-existing semantics: `os.Rename` on Unix and `MoveFileEx(MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)` on Windows.
- Commit `f8db3c44` durably copies the running executable to a separate sibling helper image before launch, so the helper does not overwrite its own loaded image. Helper launch failure preserves `current`, removes the unused helper copy, and does not trigger shutdown.
- Commit `c53e83f6` replaces Windows signal-zero polling with `OpenProcess(SYNCHRONIZE)` and `WaitForSingleObject` on a stable handle to the exact parent process. Replacement begins only after kernel-confirmed termination; timeout, cancellation, handle-access failures, and unexpected wait states fail closed. Unix retains signal-zero polling.
- Commit `17a27925` retains a shutdown contract for each successfully started successor. Failed health/version validation stops and reaps the exact `exec` process, stops systemd synchronously, or stops launchd and verifies the job is no longer running before restoring the backup. If shutdown cannot be confirmed, rollback fails closed and leaves the newly installed executable in place; a definitive launch failure can still safely restore the backup because no successor started.
- Startup repairs legacy interruption residue where the configured executable is absent but a valid committed backup remains, then safely resumes replacement. Regression coverage proves this recovery, failed publication preserving the old executable and backup, executable-mode preservation, successful replacement, mismatched-version rollback, separate helper launch identity, launch-failure cleanup, a real long-running Unix successor being stopped before rollback, failed shutdown suppressing rollback, definitive start-failure rollback, and the Windows live-child kill/reap boundary.

Latest systemd helper-lifecycle fix completed on 2026-08-02:
- Commit `db4a98f2` changes systemd-mode standalone binary updates to launch the sibling updater helper through `systemd-run --collect --property=Type=exec -- ...`, placing it in an independently systemd-owned transient service rather than the application unit's cgroup.
- `BinaryInstaller.Apply` waits synchronously for `systemd-run` to accept and start the transient service before requesting application shutdown. If transient-service ownership fails, the application remains running, the installed executable is untouched, and the unused helper copy is removed.
- Once independently owned, the helper waits for parent exit, replaces the executable, and invokes `systemctl restart` for the configured application unit from outside that unit's cgroup. Operators using systemd mode must authorize the service account to create transient services with `systemd-run` and restart/stop the target with `systemctl`.
- Regression coverage verifies transient-unit command construction, helper survival ordering, ownership-failure cleanup, executable replacement, and configured service restart.

Latest systemd post-handoff recovery fix completed on 2026-08-02:
- Commit `08c7055f` installs a recovery guard only after the helper confirms parent exit. Every later systemd helper error verifies that the configured current executable is a regular file or restores the committed backup when current is absent, then restarts the configured application service.
- Bootability and recovery-restart failures are joined with the original helper error rather than masking it. A rollback restart that already succeeded marks recovery complete and avoids an unnecessary duplicate restart.
- Regression coverage exercises missing staged content after parent exit with both an intact current executable and a missing current executable restored from its committed backup. The tests use a fake production-path `systemctl` and require the exact configured service restart plus bootable current content.

Latest launchd helper-lifecycle fix completed on 2026-08-02:
- Commit `34b1e309` changes launchd-mode standalone updates to submit the sibling updater helper through `launchctl submit -l <unique label> -- ...`, placing it in an independently launchd-owned one-shot job rather than starting it as a child of the application job.
- `BinaryInstaller.Apply` waits synchronously for launchd to accept helper ownership before requesting application shutdown. Submission failure preserves the running application and executable and removes the unused helper copy.
- After confirmed parent exit, launchd helper failures use the same manager-owned recovery guard as systemd: preserve a regular current executable or restore the committed backup, then kickstart the configured application target. A successful rollback restart suppresses duplicate recovery.
- Operators using launchd mode must authorize the service account to submit jobs with `launchctl` and kickstart/stop the configured target.
- Regression coverage verifies submitted-job command identity, acceptance-before-shutdown ordering, submission-failure cleanup, successful replacement and target kickstart, and pre-install recovery with either an intact executable or a committed backup.

Latest binary helper handoff and reconciliation fixes completed on 2026-08-02:
- Commit `f44375b3` replaces the unsafe pre-launch `pending` write with a two-phase durable handoff. `BinaryInstaller.Apply` first publishes an exact-identity `prepared` record and launches or submits the helper.
- The helper claims ownership by renaming the exact `.prepared` identity into the active outcome path, then retries durable `pending` publication. Commit `51bc5ccb` closes the claim/read race by rechecking the active outcome path after the prepared path is observed absent; claimed evidence remains fail-closed with exact-generation drain ownership.
- Commit `e8061709` makes parent shutdown require active, exact-identity, durably published `pending` evidence, so helper death after claiming `prepared` but before publishing `pending` cannot authorize parent exit.
- Commit `50cb4135` requires durable authorization from the still-waiting installer before the helper checks parent exit or performs replacement side effects.
- Commit `ca967c1b` makes installer authorization and unauthorized-helper timeout cancellation atomically compete by renaming the same durable `pending` identity to distinct winner paths. Exactly one transition can win: authorization is the handoff linearization point, while cancellation proves replacement did not begin.
- Commit `3749d0fc` handles timeout, cancellation, or platform wait errors after authorization but before confirmed parent exit. The helper first confirms or restores a bootable predecessor, then atomically renames exact `authorized` evidence to `cancelled`; staged replacement remains untouched. Systemd and launchd synchronously restart the configured predecessor service only after cancellation is durable, while exec mode avoids launching a duplicate when parent exit is unconfirmed.
- A helper that times out without authorization durably publishes exact-identity `cancelled` evidence and cannot wait for parent exit, replace the executable, or restart a successor. Startup reconciliation settles cancellation only when the exact predecessor version is running and releases only the persisted matching drain generation.
- Active `prepared`, `pending`, and `authorized` evidence remains fail-closed during restart reconciliation until exact terminal success, rollback, or safe cancellation evidence settles the owned generation. Outcome reads recheck the authorization winner path to close the rename/read classification race.
- Regression coverage proves a late `pending` acknowledgment cannot act after installer timeout, an unauthorized helper publishes cancellation without changing the executable, authorization and cancellation have exactly one atomic winner, authorized parent-exit timeout and context cancellation preserve the predecessor and publish exact cancellation, manager recovery starts only after terminal evidence, and a restarted predecessor autonomously reconciles cancellation for the exact generation.

Latest post-authorization helper-death recovery implementation on 2026-08-02:
- Commit `ce38c11f` gives each exact helper operation an OS-held lease on Unix and Windows, allowing startup reconciliation to distinguish a live helper that may still act from a dead helper whose residue can be recovered.
- The helper durably records `parent_exited`, `backup_published`, `target_published`, `validating`, and `rolling_back` boundaries. Restarted helpers resume from these phases without replaying completed filesystem side effects; durable rollback intent remains authoritative even if a target process is observed.
- Systemd transient helpers use `Restart=on-failure`; launchd submitted jobs retain their documented failure-restart behavior. Manager retries exit cleanly after a terminal outcome instead of looping.
- Startup reconciliation safely settles an untouched predecessor or exact validated target. Ambiguous published-target and rollback residue launches an independently owned recovery-only sibling helper, waits for durable readiness before parent shutdown, and retains exact-generation drain ownership until `succeeded`, `cancelled`, or `rolled_back` is durable.

Latest atomic dead-helper recovery handoff fix completed on 2026-08-02:
- Commit `195b56d8` makes the coordinator retain the exact-operation OS helper lease through terminal outcome publication and exact-generation drain cleanup, preventing a systemd/launchd-restarted original helper from reacquiring ownership before terminalization.
- Ambiguous recovery persists an exact durable recovery-ownership claim while the coordinator still holds the lease, then releases the lease so only a recovery-mode helper can acquire it and continue. Manager-restarted original helpers that observe the claim exit without replacement side effects; missing, malformed, or mismatched claims fail closed.
- Regression coverage deterministically reproduces the former post-probe reacquisition race, verifies recovery ownership is durable before lease transfer, and verifies original manager retries cannot act after transfer.

Latest direct-exec fail-closed fix completed on 2026-08-02:
- Commit `92e3292a` rejects direct-`exec` automatic replacement at configuration, apply, and recovery boundaries. Rejection occurs before helper artifacts are created, a process is launched, or application shutdown is requested.
- Only independently manager-owned systemd and launchd restart modes are supported for automatic standalone binary updates. The obsolete sibling-supervisor entrypoint and worker-only regression were removed, and operator documentation now reflects the supported contract.
- Regression coverage proves unsupported exec replacement and recovery fail without helper publication, launch, or shutdown. Existing handoff tests use manager-owned modes.

Current audit status on 2026-08-02:
- A fresh separate strictly read-only audit of checkpoint `195b56d8` inspected the implementation and workspace directly while ignoring PRs.
- The audit found no material bugs, regressions, or missing runbook requirements. It specifically confirmed race-safe dead-helper terminalization and recovery ownership transfer, manager-restarted original-helper exclusion, exact-identity fail-closed evidence handling, persistence and rollback boundaries, manager supervision, and Unix/Windows behavior.
- No builds, tests, formatters, generators, edits, or other modifying commands were run during the audit; the workspace remained clean and unchanged at `195b56d851a31b1213718d60402ae446897421c9`.

Validation completed after commit `195b56d8` on 2026-08-02:
- Deterministic lease-transfer regressions, repeated focused stress runs, the complete update suite, and the update race suite passed.
- Native build, Linux and Windows update-package cross-compilation, and native/Linux/Windows scoped vet passed.
- `make templ` produced zero updates, the uncached full repository suite passed, post-commit focused checks passed, and the worktree was clean.
- Repository-wide `go vet ./...` remains blocked only by four pre-existing self-assignment diagnostics in untouched `internal/agentlibrary/applier.go`; the established non-failing macOS object-version linker warning also remains.
