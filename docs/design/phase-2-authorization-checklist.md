# Phase 2 authorization verification checklist

The unreleased Phase 2 database changes are consolidated in `224_project_authorization.sql`, upgrading directly from version 223. Tests cover upgrade, rollback, and re-upgrade from that released boundary. Rollback removes the Phase 2 schema while preserving existing source records; no external-user bindings are inferred. Earlier incremental Phase 2 migrations are consolidated; main’s migrations through 223 are preserved.

Phase 2 is incomplete. This is the remaining verification ledger, not a claim that existing policies cover every entry point. Evidence for individual changes is recorded in [teams/orgs design checkpoints](teams-orgs.md). A surface closes only after its production wiring, role boundaries, cross-project behavior, revocation, auth-off behavior, and relevant tests are verified.

| Surface | Existing evidence | Remaining verification / implementation |
| --- | --- | --- |
| Shared permission service and migration | Current-role resolution and org-wide editor compatibility tests exist | Inventory every caller and missing-identity fallback; verify complete migration/auth-off suite |
| HTTP/JSON | Guards for sharing, Insights, channel settings/allowlists and global administrative actions | Enumerate every registered route and resource-ID resolver; test method, role and cross-project matrix |
| Runtime tools | Project policy checks; Slack binding checks before tool calls; production GitHub runtimes re-resolve current users, pin task/execution identity and recheck saved goal/channel/automation provenance on every invocation; pure reads require viewer access and mutations/activity-recording lists require maintainer access | Inventory every tool and downstream persistence boundary; test revocation between tool admission and write |
| Lists/selectors | Project filtering and transactional Slack selection checks | Verify all page, JSON, channel and tool selectors, stale selections and foreign resource IDs |
| WebSockets | Subscription, replay and revocation checks have targeted coverage; every application frame revalidates the session, and idle sockets check expiry/logout every second (including legacy authentication without organization notifications) | Verify every event type and reconnect/replay path; revoked sessions and memberships while connected |
| Sharing/member/ownership UI | Grants, ownership controls and eligibility tests | End-to-end role matrix, stale forms, ownership transfer, removal and audit visibility |
| Workers | Saved run-as and execution snapshots; active authorization watcher; authenticated worker/follow-up checks reject unmapped Telegram, Discord and email task origins, including watcher cancellation | Inventory specialized entry points and legacy recovery; cancellation tests for each provenance source |
| Schedules | Independent user/Slack/Discord provenance; calendar and automation admission checks | Verify all schedule mutation/recovery paths and live revocation across dispatched work |
| Goals | Exact-goal/user snapshots; continuation and mutation checks | Inventory all automatic and manual continuation routes, replacement and cancellation races |
| Webhooks | Stronger role checks from durable task origin | Complete ingress identity/provenance, signature/replay, recovery and active revocation audit |
| Automation publication/invocation/dispatch | Publisher/initiator identities with Slack/Discord provenance and launch archives; queued worker/continuation checks; graph-save and registered-publication role, provenance and atomic audit checks | Other specialized dispatch mechanisms, legacy missing identities and all graph-deletion consumers |
| External automation actions | Reservation/release and GitHub lease admission/dispatch/reuse checks; archived launch evidence; label provisioning follows admission and reused-issue label repair rechecks after provider reads; production GitHub mutation calls recheck current roles and saved execution provenance after intervening reads, retaining optional PR body/branch capabilities; assigned-issue discovery rechecks after provider reads and transactionally validates launch authority with atomic audits; feedback queue transactions validate PR/task/project scope, identified caller role and atomic handoff audits; production forwarding rechecks pinned execution provenance before fetches and each handoff, including within queue transactions; queued feedback independently retains the source execution’s Slack sender binding for promotion/recovery; reviewer allowlist trust is rechecked transactionally before admission or deduplication and again at queued promotion, steering conversion, preparation and local commit; independent input/execution reviewer snapshots survive PR/input cleanup, and promoted execution recovery/startup/worker watching recheck them | Remaining provider handoff race boundaries, other projection/outcome recording, full execution/source relationship validation and other providers |
| GitHub reviewer identities | Project/host/numeric-ID admin attestations; authenticated feedback queue and execution snapshots; permission, rebinding, source-cleanup and active-worker cancellation tests | Historical instructions without immutable identity evidence; unpinned direct callers; complete specialized-consumer inventory |
| Slack channel | Admin-attested workspace/user/project mapping; task/input/execution/schedule/automation provenance | Inventory all non-receipt actions and downstream mutations; verify every active/recovered run path |
| X channel | Installer and sender identity mappings; polling cancellation and receipt transaction checks | Complete per-action and deferred provenance/recovery inventory |
| Telegram/Discord/email channels | Admin allowlist mutations, transactional credential saves and audits; Discord project-scoped binding persistence and explicit admin-attestation settings controls; task/input/execution/consumed-steering/schedule/invocation/publisher provenance and focused revocation tests | Telegram/email sender mappings and provenance remain unfinished. Discord swarm/specialized mutation paths and complete ingress/runtime wiring remain unfinished. Telegram's installation-wide allowlist and username backfill are not application-user bindings. Authenticated Telegram, Discord and email now reject ingress, direct chat handoff, selectors and runtime actions until verified mappings exist; email also blocks polling and mailbox side effects. Auth-off retains the legacy allowlists. Implement mappings and durable provenance before enabling authenticated channel ingress, and separately audit existing deferred/running work. |
| Audit | Atomic records at implemented mutation boundaries | Inventory remaining mutations, secret exclusion, access to audit reads and failure behavior |
| Compatibility and isolation | Private-project creation remains disabled; auth-off owner compatibility retained | Re-run production restriction and migration coverage after final wiring; Phase 3 process/filesystem sandboxing is still required |

## Current focused checkpoint

GitHub external-activity cleanup now retains the caller and Slack provenance when detaching cancellation at all six call sites. A real-store service regression checks that cancellation alone permits authorized cleanup, while a forged owner claim and a removed Slack binding preserve reservations. Post-provider lease completion now retains caller context and records its outcome audit atomically, without treating current revocation as grounds to hide an already-dispatched effect. Identical retries do not duplicate audits and conflicting results are rejected. Projection repair remains a separate outcome-recording audit item.

GitHub issue lease release reads the saved launch source, checks current publisher/initiator/caller authority (including live or archived Slack bindings), deletes the reserved lease and writes an identified audit in one transaction. Wrong scope/token and dispatched/completed leases retain the existing non-releasable error contract. Revoked authority or failed auditing must preserve the reservation. This does not undo a remote action or make a provider request atomic with authorization.

Do not check off any full surface solely from this checkpoint. Production private-project creation remains unavailable until Phase 3; application policy does not provide process or filesystem isolation.

Queued GitHub feedback now rechecks the reviewer recorded in its linked feedback row before consumption, including anonymous legacy instructions. Tests cover removal at promotion, conversion, preparation and commit without request context. This is external allowlist trust, not a verified reviewer/application-user mapping. Independent reviewer input/execution snapshots now survive feedback/PR deletion. Promoted executions are checked at startup/recovery and by the active worker watcher; committed local steering adds reviewers to that execution snapshot. Verified application-user mappings and a complete audit of native-provider consumption and specialized recovery remain open.

Native-provider steering acceptance now copies reviewer evidence for pending, ambiguous and confirmed delivery in the same transaction as the delivery record. The legacy MarkApplied callback also snapshots evidence and rejects mismatched execution/task/project scope. These are outcome-recording boundaries: late revocation does not hide a provider acceptance, but the recorded execution evidence makes current-access checks reject further work. Migration 224 backfills known native acceptance evidence and excludes unsent instructions. Reviewer application-user mapping, pre-send provider checks and a full specialized-consumer inventory remain open.

OpenAI and Anthropic agentic sends now invoke a shared production callback at HTTP request construction (and OpenAI websocket stream opening). Worker and chat runtimes supply a transactional check of the saved execution, prepared input scope/authorship and channel/reviewer provenance, including history-only continuations. Tests revoke authority during tool filtering and verify zero transport calls. Remaining work includes other provider implementations/reconnect internals, reviewer-to-application-user mappings, and the full specialized dispatch inventory.

OpenAI-compatible chat-completions now runs the production send guard before custom authentication finalization and again afterward, on every rebuilt request. Responses HTTP fallback checks each request build; websocket transport checks before connection setup and immediately before the instruction frame, closing a newly established/reused connection on denial. Tests cover finalizer revocation, stream retry revocation and an actual handshake-time revocation with no instruction frame. Reviewer-to-user mappings and complete non-agentic/other-provider execution coverage remain open.

GitHub reviewer mapping investigation exposed unguarded installation-wide trust-list mutations. HTTP middleware and direct handlers now require current org-admin authority for create/update/remove and project visibility for the settings read. Identified repository mutations repeat the current-admin check inside the write transaction and audit atomically under the default project's installation settings scope. Duplicate updates cannot bypass the guard. This secured a prerequisite; reviewer bindings were not yet implemented at that checkpoint (superseded by migration 224 below). Project inbox mutation authorization and anonymous embedded-repository caller inventory remain separate audit items.

## Reviewer application identity checkpoint (migration 224)

New authenticated GitHub runtime feedback now requires an explicitly admin-attested binding for the selected project, GitHub host, immutable numeric provider user ID, and application user. The settings form requires confirmation; handler and transaction boundaries recheck current admin authority. Binding writes and their identity details are audited atomically. Provider comment/review parsers preserve numeric IDs, and queue admission matches the provider host against the persisted PR host. Login trust alone cannot authorize a new authenticated handoff.

Admission checks the reviewer's current task permissions independently of the forwarding executor. The exact binding is snapshotted on the input and copied on promotion/local/native steering consumption to execution evidence. Rebinding, removal, disabled accounts, departed memberships, and insufficient task roles deny subsequent consumption/recovery; the existing worker watcher checks execution snapshots. PR/input cleanup cannot erase execution identity evidence. Auth-off forwarding retains its previous allowlist contract. Migration 224 preserves legacy allowlists without guessing identities.

This closes the new authenticated feedback mapping path, not the full reviewer/background inventory. Historical inputs/executions without immutable provider evidence still have only their existing login-trust checks; upgrading those safely with auth-mode-aware recovery remains open. Other channels, project inbox authorization, direct/embedded forwarding callers without pinned runtime metadata, all specialized consumers, and the full surface matrix above remain unfinished. Private-project creation stays disabled; Phase 3 sandboxing is still required.

## Project inbox configuration checkpoint

The GitHub project-inbox POST handler now requires current maintainer/owner access for the exact form project before reading or changing configuration, including the empty-login disable branch. Identified repository upserts repeat that check inside the write transaction and atomically record `github.inbox_updated`. Forged role claims, departed members and foreign-project requests cannot borrow a prior selection's authority. Audit failure preserves the previous enabled flag and assignee. Auth-off handlers explicitly resolve the owner; unconfigured anonymous embedded repository calls retain their legacy contract.

This secures inbox configuration, not the entire scheduled scanning lifecycle. The selection is not a reviewer/application-user binding. Historical feedback recovery, other channel mappings, missing-context embedded callers, and the remaining surface inventory above still require work. Private-project creation remains disabled pending Phase 3 sandboxing.

## Historical reviewer recovery policy checkpoint

Authenticated worker recovery now carries a server-owned requirement for captured reviewer/application identity. For every known historical reviewer on an input or execution, persistence requires matching immutable identity evidence; an allowlist entry or a mapping created after admission is insufficient. The policy follows recovered worker contexts into consumption and provider checks and is applied again by the active watcher and GitHub runtime authorization, including fresh caller contexts. Auth-off workers retain legacy login-trust recovery and do not erase an already stricter context.

The real-store regression covers leaving a historical input pending, auth-off promotion, authenticated recovery/startup denial, history-only provider delivery denial, fresh-owner runtime denial, and cancellation of an already-running legacy execution. New mapped reviewer execution/rebinding coverage remains enabled in authenticated mode. This does not reconstruct deleted historical evidence or finish standalone chat/follow-up policy propagation, all specialized consumers, or the other channel identities. Phase 2 remains incomplete; production private projects remain disabled pending sandboxing.

## Standalone follow-up recovery checkpoint

Standalone streaming creates its base context with the authenticated reviewer-identity requirement; follow-up user restoration and per-call access checks apply it too. This carries the policy through queued promotion, streaming/provider callbacks and watcher ticks without relying on worker-owned contexts. Historical reviewer instructions remain consumable in auth-off mode under existing trust checks, but authenticated follow-ups reject absent captured reviewer identities even if an owner calls directly or an admin later adds a binding.

Execution checks no longer depend on finding a saved executor row. Auth-off legacy executions still undergo exact task/execution and provenance validation; authenticated runs require a saved executor. A configured project authorization handler with an execution ID but no run-identity store fails closed. Focused tests exercise historical queue denial, auth-off promotion, authenticated startup/provider denial and watcher cancellation, plus foreign legacy execution rejection and authenticated missing-executor denial.

Other channel identity mappings, specialized consumers, the historical-evidence inventory and the remaining route/tool/UI/background matrix are still open. This checkpoint does not authorize private-project creation or imply process/filesystem isolation.

## Discord unmapped-ingress containment checkpoint

Production Discord receives the authorization-configured worker. In authenticated mode its installation-wide external allowlist cannot establish an application principal: gateway and direct message ingress now stop before project selection, chat persistence or handoff. Cached/fallback selectors return no project, selection writes fail, and every Discord runtime action checks the configured policy at invocation. Even an owner principal or a missing allowlist store cannot bypass this boundary. Auth-off and unconfigured embedded services retain the existing behavior.

Focused race tests cover cached/foreign selections, unchanged selection persistence, gateway hooks, every runtime handler, policy changes after runtime creation, and a fully configured direct-ingress flow that runs only in auth-off mode. Discord/channel-trust service and handler regressions and the server suite pass. This is containment, not a verified Discord mapping implementation: explicit project-scoped sender mappings, durable deferred-work provenance, active-run revocation and outbound paths remain open. Email requires a separate audit. No private-project or isolation policy changed.

## Email unmapped-ingress containment checkpoint

Email's global sender allowlist and persisted project selection do not establish application identity. The production authorization-configured worker now causes authenticated polling, mailbox seen-marking, poll side-effect admission, direct ingress, selectors and email runtime actions to fail closed. This precedes provider connection, custom handoff hooks and chat admission. Supplied owner principals and cached selections do not bypass the policy. Auth-off retains the configured allowlist/chat flow.

The focused real-store test covers no mailbox connection, no handoff, unchanged saved selection, every email runtime handler, and successful auth-off handoff followed by denial when authenticated mode is restored. This is containment only: verified email sender mappings, spoof-resistant identity evidence, durable deferred-work provenance, running-work cancellation and outbound authorization remain unfinished. No project migration, private-project availability or isolation guarantee changed.

Validation: focused email identity-boundary race coverage, email/channel-trust service and handler regressions, and the server suite pass.

## Unmapped channel recovery containment

Authenticated workers now reject task origins Telegram, Discord and email before admission or recovered execution authorization. A saved application owner does not substitute for a verified sender. The shared check also runs on active-worker watcher ticks. Standalone follow-ups reject these persisted origins and channel reply sources before execution access and through their watcher; a browser task cannot supply authority for an unmapped channel reply. Auth-off retains legacy recovery.

This is origin-based containment, not durable sender mapping. It does not reconstruct missing historical origins, establish identities for channel instructions on unrelated tasks, or finish every direct runtime/provider path. Verified mappings and complete task/input/execution provenance remain required, alongside the remaining Phase 2 checklist. Private-project creation remains disabled pending Phase 3 sandboxing; no process/filesystem isolation is claimed.

Pinned GitHub runtime calls also reload the task and reject these origins before provider actions in authenticated mode, without waiting for watcher cancellation. Auth-off keeps the existing execution/provenance checks and permits these legacy origins.

Validation: focused worker/follow-up recovery and cancellation tests pass with race detection for all three origins; GitHub runtime, worker, follow-up and channel service/handler regressions and the server suite pass. Auth-off recovery and runtime access are explicitly covered.

## Discord binding persistence foundation (migration 224)

The Discord authorization store now supports explicit application-user binding of an existing project-scoped numeric Discord user ID. Binding requires a current organization administrator inside the transaction; role claims, missing callers and foreign-project allowlist IDs cannot authorize it. The target must currently have editor, maintainer or owner project access. The binding and `discord.sender_bound` audit (including both identities) commit atomically. Resolution joins the exact project and external ID and rechecks current membership/disabled state and editing access in the same database transaction. Removing the allowlist entry removes its live binding.

The migration creates no bindings for existing entries, preserves allowlists on downgrade, and does not change inherited org-wide editor access. Tests exercise explicit attestation authorization, wrong project/ID, mutable or noncanonical external IDs, persistent resolution, audit-failure rollback, downgrade, disabled users and removed memberships. This is a persistence foundation only: administration UI/HTTP controls, ingress handoff checks, immutable queued/execution snapshots and active binding-revocation handling remain unimplemented for Discord. Authenticated Discord containment stays enabled; this change does not enable ingress or private projects. Phase 2 remains incomplete and Phase 3 sandboxing remains required.

Validation: Discord binding persistence race tests, legacy-preserving migration round-trip and full database migration suite, Discord/channel-trust service and handler regressions, and server tests pass.

## Discord identity administration controls

Discord settings now display each entry's saved application user (or unbound state) and provide an explicit attestation checkbox and binding action. The new POST route and direct handler require current organization-admin authority and access to the exact form project; missing attestation or user ID is rejected. Persistence retains its transactional administrator, target-role and audit checks. Project list rendering loads binding status in one additional scoped query. Auth-off resolves the default owner through the existing shared handler policy.

Tests cover missing caller/attestation, foreign project IDs, saved binding display, stale admin claims after demotion/removal, auth-off binding, and HTMX target/include controls. Authenticated ingress deliberately remains disabled: bindings are not yet carried through Discord admission, queued/execution snapshots or active revocation checks. The UI states this limitation. This checkpoint adds no migration and does not change private-project availability or provide process/filesystem isolation.

Validation: handler/persistence binding race tests, Discord/channel-trust service and handler regressions, rendered settings/HTMX tests, and server tests pass. Initial linking failed due to disk exhaustion; reruns passed after bounded removal of approximately 3 GiB of old regenerable Go cache files.

## Discord task admission provenance (migration 224)

Trusted Discord ingress metadata now has a typed context carrier. Shared task creation validates its exact project/external-user/application-user binding and current project permissions inside the task transaction. Missing/substituted principals and foreign projects fail closed; auto-merge and privileged origins require maintainer/owner access. Task creation saves an independent Discord sender snapshot alongside the run-as identity and `discord.task_created` audit atomically. Audit failure rolls back all admission writes.

Task-user recovery rechecks that snapshot without request metadata. A changed or deleted live binding denies recovery, and the denial evidence survives allowlist deletion. Explicit run-as rebinding cannot replace the saved sender or erase its provenance. Migration 224 preserves historical tasks without inventing sender evidence.

This is not end-to-end Discord admission: the production service remains blocked in authenticated mode. Queued inputs, execution snapshots, schedules, steering, downstream mutations and active binding-revocation checks still need wiring before that block can be removed. Existing origin-based containment remains in place. Auth-off tasks without these new snapshots keep their legacy path; production private projects remain disabled pending Phase 3 sandboxing.

Validation: Discord task admission/recovery race coverage, migration round-trip/full database suite, Discord/run-identity/task-admission persistence/service/handler regressions and server tests pass. Tests include captured-context rebinding, owner substitution, missing identity, foreign project, privileged task denial, audit rollback and source-deletion recovery denial.

## Discord execution provenance (migration 224)

Ordinary identified execution admission now validates captured Discord identity or inherits and rechecks the task's saved binding, without relying on ingress request context. Missing principals cannot take the legacy execution path for bound tasks. Admission saves an independent execution sender snapshot in the same transaction as execution identity and its existing audit. Snapshot-write failure rolls back execution creation.

Shared execution recovery checks and MarkRunning validate the saved executor, current exact binding, project scope and task permission requirements. Execution evidence survives removal of task provenance and the live allowlist. The existing active-worker watcher uses these checks; a real-store test confirms binding deletion cancels a running execution even on a task without a channel origin, and a fresh owner cannot recover it. Historical executions receive no invented evidence on migration.

Authenticated Discord ingress remains disabled. Queued/deferred inputs, steering, schedules, specialized execution admission and downstream mutation paths remain unverified/unwired for Discord; this checkpoint does not close those paths or the overall Phase 2 inventory. Auth-off legacy executions without binding snapshots retain their existing behavior. Production private-project creation remains disabled pending Phase 3 sandboxing.

Validation: execution admission/recovery/rollback and active-worker cancellation tests pass with race detection; legacy no-provenance execution and worker auth-off tests pass. Discord/run-identity persistence/service/handler regressions, full migration suite and server suite pass.

## Discord queued-input provenance (migration 224)

Input admission with trusted Discord context now requires the exact project/sender/application-user binding, current task permissions and matching input author. It saves an independent sender snapshot and `discord.input_admitted` audit in the input transaction. Shared input-provenance validation rechecks the saved binding without request metadata. Queued promotion transfers it to the execution snapshot after establishing the executor identity, in the same transaction; revocation or audit failure leaves the input pending and creates no execution.

The queue test starts from a task without Discord task provenance, proving that the input carries independent evidence. It covers missing principal, foreign project, mismatched external ID, admission-audit rollback, removal before promotion, promotion-audit rollback, successful recovery without ingress context, and removal after promotion blocking startup/recovery. Migration 224 preserves historical inputs without inferring bindings.

Authenticated Discord ingress remains disabled. Steering acceptance/consumption must still copy durable Discord evidence into already-running executions; schedules, specialized dispatch, downstream mutations and the complete channel ingress/service wiring remain open. These changes do not enable private-project creation or establish process/filesystem isolation. Phase 2 remains incomplete.

Validation: focused queued-input race tests, full database migration suite, Discord/thread-input/steering persistence/service/handler regressions, and server tests pass.


## Discord consumed-steering provenance (migration 224)

Local steering commit now copies each consumed Discord sender into an independent execution evidence table, atomically with consumption and its audit. Current input authorization is rechecked before local consumption. Native-provider pending, ambiguous and confirmed acceptance, and the legacy applied callback, preserve the same evidence when recording already-sent instructions, including outcomes recorded after binding revocation. A callback cannot attach the evidence to another execution of the same task.

Shared execution authorization and startup checks validate every consumed sender's current binding and task permissions independently of the executor's identity. Evidence survives input cleanup. Migration leaves Discord sender evidence empty: no verified Discord identity existed before this feature. Auth-off inputs without captured Discord provenance keep their prior path.

Verified: steering race tests cover late local revocation, outcome recording after revocation, audit rollback, wrong-execution callbacks, and recovery/startup rejection after input deletion. Full database migrations, Discord/steering/thread-input persistence/service/handler regressions and server tests pass. Active watcher use of the shared execution check is existing wiring; this checkpoint does not add a separate consumed-steering watcher integration test.

Phase 2 remains incomplete. Discord schedules, specialized dispatch, downstream mutations and complete ingress/runtime wiring remain open; authenticated Discord ingress stays disabled. Production private-project creation remains unavailable pending Phase 3 sandboxing. These authorization records do not provide process/filesystem isolation.


## Discord ordinary schedule provenance (migration 224)

Ordinary schedule creation with captured Discord identity now validates the exact sender binding and current task permissions and saves an independent schedule snapshot in the same transaction as the run-as identity and existing `schedule.run_as` audit. Missing principals cannot use anonymous creation with captured sender metadata. Recovery validates the saved schedule user and binding without ingress context; rebinding the task does not replace the schedule evidence.

Ordinary calendar admission rechecks the schedule snapshot transactionally and copies it into the execution snapshot, including the calendar path that creates executions directly. Binding removal leaves an occurrence unclaimed; dispatch audit failure rolls back the occurrence and execution snapshot. After dispatch, startup and recovery use the execution evidence.

Coverage includes creation-audit rollback, missing principal at creation/admission, independent schedule identity on a task without Discord provenance, task resubmission, revocation before and after dispatch, and dispatch-audit rollback. Migration leaves historical schedules unbound and preserves their configuration across downgrade/reupgrade.

Still open: automation-generated schedule publication/admission, swarm planner dispatch, remaining specialized paths and downstream mutations, and end-to-end Discord ingress/runtime wiring. This checkpoint does not complete Phase 2 or enable authenticated Discord ingress. Auth-off schedules without sender provenance retain the existing path. Production private-project creation stays disabled until Phase 3 sandboxing; permission tables do not provide process/filesystem isolation.

Validation: focused Discord schedule race tests, full database migration suite, Discord/schedule/calendar persistence/service/handler regressions, and server tests pass.


## Discord automation schedule/invocation provenance (migration 224)

Automation publication with captured Discord identity now validates its current binding and maintainer/owner access before graph writes, including denial when a principal is missing. Newly generated schedules retain the publisher's Discord schedule snapshot in the publication transaction; publication audit failure rolls it back. Republishing an existing schedule does not replace its original sender identity.

Scheduled admission rechecks that saved binding before new or reused occurrences. Manual and scheduled invocation admission save independent Discord sender provenance with the invocation identity and existing admission audit. Dispatch leasing consumers, durable envelope recovery, execution authorization, continuation checks and source-bound automation actions recheck this invocation evidence. Schedule-provenance cleanup cannot erase the invocation's checks. Graph replacement archives the Discord initiator sender alongside existing launch authority so current binding and user checks continue after the live graph is deleted.

Targeted tests cover publication and invocation-snapshot rollback, anonymous captured metadata, occurrence preservation after revocation, dispatch denial before execution creation, recovered execution/reuse denial, archived scope and graph-deletion rollback, and cached envelope identity substitution. A real worker test checks binding removal after MarkRunning and cancellation by the authorization watcher. Migration does not infer identities for historical invocations.

Remaining Discord work includes publisher-version provenance independent of invocation identity, swarm planner dispatch, downstream mutations, complete ingress/runtime wiring, and the rest of the Phase 2 surface inventory. Authenticated Discord ingress remains disabled. Existing auth-off calls without captured channel identity keep the legacy path. Production private-project creation remains disabled pending Phase 3 sandboxing; application policy does not provide process/filesystem isolation.

Validation: focused persistence and active-worker race tests, full database migration suite, automation/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Discord publisher-version provenance (migration 224)

`SaveCurrentGraph` now saves a Discord publisher snapshot in the same transaction as the version, publisher run-as identity and publication audit. The snapshot is independent of any later invocation sender. Shared publisher authorization checks the saved application user, exact project/sender binding and current maintainer/owner access before admission, dispatch, continuation and execution recovery. A forwarding owner cannot replace a downgraded or rebound publisher.

Graph replacement archives the publisher's Discord sender alongside launch authority before cascading live version deletion. Archived provider-action authorization rechecks the original publisher binding and current permissions; initiator evidence is preserved separately. Existing historical versions and archives receive no inferred Discord identity.

Tests cover snapshot failure rollback, missing principal, publisher downgrade and rebinding, denial before occurrence/dispatch creation, recovered execution/startup rejection, and archived scope/revocation checks after graph deletion. Archive failure rolls back both graph deletion and the new evidence. Migration tests cover upgrade/downgrade/reupgrade without inventing identities. No new publisher-specific active-watcher integration test is claimed here; the shared execution validator is the existing watcher integration.

Phase 2 remains incomplete. Discord swarm dispatch, alternate registration/publication paths, downstream mutations and complete ingress/runtime wiring still require verification, along with the remaining full checklist. Authenticated Discord ingress stays disabled. Calls without captured Discord provenance retain their existing auth-off path. Production private-project creation remains disabled until Phase 3 sandboxing; policy tables do not provide process/filesystem isolation.

Validation: focused publisher/publication/archive race tests, full database migration suite, automation/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Registered automation publication authorization

`PublishRegistered`, the alternate registration publication path, now checks identified callers' current maintainer/owner access inside its immediate transaction. Captured Slack/Discord identities are validated before graph reads or writes; missing matching principals and foreign Discord project metadata are denied. New versions save publisher run-as identity and channel provenance atomically with an `automation.registered` audit. Existing-version reuse rechecks the saved publisher and writes `automation.registration_reused` with normalization/cleanup; it does not substitute the new caller for the original publisher. Both graph-cleanup branches archive launch authority before deleting old versions.

Focused tests cover forged owner claims on an editor, maintainer registration, missing principal, foreign project, publication audit rollback (including schedule enablement), reuse-audit rollback of graph normalization, publisher retention on owner reuse, downgrade and binding removal. Legacy anonymous registered-publication tests continue to exercise bootstrap/auth-off compatibility. This repository has no auth-mode configuration: anonymous calls without captured channel metadata remain the legacy internal path, so production entry-point identity enforcement must still be verified rather than inferred from this change.

This supersedes the outstanding `PublishRegistered` persistence gap in the previous checkpoint, not the whole publication/service inventory. Discord swarm dispatch, downstream mutations, complete authenticated ingress/runtime wiring, and the broader Phase 2 requirements remain open. Authenticated Discord ingress stays disabled. Production private projects remain disabled until Phase 3 sandboxing; no process/filesystem isolation is provided by these policy tables.

Validation: focused registered-publication role/provenance/rollback race tests and legacy registration tests, automation/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Scheduled swarm planner admission

`ReserveCalendarPlanner` now reloads the calendar admission's saved user and schedule ID under the write lock, rejects a substituted caller, and checks the admitted user's current parent-task permissions before planner preparation. Saved Slack/Discord schedule bindings are revalidated without request context. Planner callbacks must return a child in the parent project with the correct parent and planner role; existing planners are also checked for project scope.

The planner execution now uses identified admission, saving run-as identity and independent channel provenance. A `schedule.planner_admitted` audit commits with the execution, planner creation/state changes, parent blocking and admission transfer. Tests cover binding removal before the preparation callback, substituted caller, foreign-project planner callback, full rollback on audit failure, and execution startup/recovery denial after schedule-provenance cleanup and binding removal. Legacy anonymous calendar planner admission remains compatible and has an explicit test.

This closes the scheduled calendar-to-planner handoff only. Manual swarm starts, subsequent worker/reviewer/merger dispatch and provenance inheritance, downstream mutations, and other original Phase 2 surfaces still need end-to-end verification. No dedicated active-watcher test for this new planner handoff is claimed; execution provenance uses the existing shared validator. Authenticated Discord ingress stays disabled. Production private-project creation stays disabled pending Phase 3 sandboxing; application authorization does not establish process/filesystem isolation.

Validation: focused calendar planner authorization/rollback/legacy-compatibility race tests, swarm/schedule/calendar/Discord persistence/service/handler regressions, and server tests pass.


## Discord swarm child inheritance and manual planner preflight

Task creation for planner/worker/reviewer/merger children now reloads a bound parent's database-owned Discord identity under the task write transaction. It requires matching parent/child project scope, parent run-as user, current binding and task permissions; supplied caller or sender metadata cannot substitute another identity. New child tasks retain independent snapshots and existing `discord.task_created` audits. Child creation with a goal uses the same recovered principal for the goal identity. Parents without Discord evidence retain their existing legacy path.

Production manual planner startup now resolves and checks the parent's saved run-as identity through worker authorization before clearing cancellation or changing parent category/status. Child execution admission subsequently uses the persisted child evidence. Focused tests cover each child role, missing request context recovery, foreign project and identity substitution, binding removal before creation, audit rollback, child-goal identity, parent-snapshot cleanup, and revoked child execution recovery/startup. A service test verifies that an owner cannot start a revoked parent's planner and that parent state remains unchanged.

Remaining work: existing unbound children, all child reuse/rerun/dispatch variants, transaction boundaries around manual-start state changes after preflight, non-Discord provenance inheritance, downstream mutations, and the full original Phase 2 checklist. These tests do not claim atomicity across all service-level swarm operations. Authenticated Discord ingress stays disabled; production private-project creation stays disabled pending Phase 3 sandboxing. Policy tables do not establish process/filesystem isolation.

Validation: focused four-role child inheritance and manual planner preflight race tests, swarm/Discord/schedule/goal persistence/service/handler regressions (including existing legacy swarm behavior), and server tests pass.


## Swarm child reuse and rerun preflight

Existing planner reuse now checks the child’s saved run-as identity before parent cancellation/category/status changes. Reviewer and merger reruns independently validate parent and child identities before state changes. Ready-worker dispatch verifies parent/child project and relationship scope and both saved identities before submission; runnable-child submission also rechecks the child before category/status writes. A valid parent or owner caller cannot substitute for a revoked child's sender binding.

Focused race tests cover planner, worker, reviewer and merger children with independently revoked Discord bindings under a still-authorized parent, unchanged persisted parent/child state on denial, and foreign-project child rejection. Swarm/Discord/schedule/goal persistence, service and handler regressions (including legacy swarm behavior), and the server suite pass.

These are service preflight checks, not an atomic transaction spanning swarm state changes and submission. Revocation between preflight and downstream writes, other completion/callback dispatch variants, historical children without provenance, non-Discord inheritance and the full remaining Phase 2 inventory still require verification. Authenticated Discord ingress remains disabled. Production private-project creation remains disabled pending Phase 3 sandboxing; application policy does not provide process/filesystem isolation.


## Guarded swarm reuse/rerun state writes

Configured planner reuse and reviewer/merger rerun admission now pin both parent and child task IDs, projects and saved users in a server-owned context. Category, status and swarm-field repositories recheck every pinned task's current run-as identity, role and saved Slack/Discord provenance inside each write transaction. A revoked child blocks a parent write as well as its own write. Successful guarded writes record `swarm.category_updated`, `swarm.status_updated` or `swarm.fields_updated` atomically; audit failure rolls back the affected write. Explicit auth-off legacy owner fallback remains subject to project scope and current permission checks.

Tests cover revocation after service preflight, independently revoked children under valid parents, unchanged task state after denial, audit rollback for all three write types, foreign scope, missing saved identity denial and auth-off owner compatibility. These guards apply to the configured reuse/rerun context; they do not make the entire multi-write rerun atomic. Other callbacks, manual new-planner state changes, cancellation flags, unguarded repository callers, historical missing provenance and the broader Phase 2 inventory remain open. Authenticated Discord ingress remains disabled. Production private-project creation remains disabled pending Phase 3 sandboxing; policy tables do not establish process/filesystem isolation.

Validation: focused persistence and service integration race tests, swarm/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Completion-triggered reviewer and merger dispatch

Worker/reviewer completion callbacks now validate the saved identities of the parent, completed source child and successor before preparing reviewer/merger work. The combined identity guard follows category/status/swarm-field writes, so each transaction checks the source as well as the destination and audits successful guarded changes. Previously these callbacks ignored write errors and could submit the successor anyway; they now return the error and do not submit. Both source and successor are rechecked again before submission. Recording a completed or cancelled outcome remains separate from authorizing new work.

Real-store callback tests cover reviewer and merger successors, independently revoked source and successor bindings, and revocation injected between service admission and the first persistence write. Denial preserves successor state and produces no worker submission. This does not make the entire callback atomic: later failures can leave earlier committed state changes. Whole-transition atomicity, other callback/planner paths, historical unbound tasks and the remaining original Phase 2 surface inventory remain open. Authenticated Discord ingress remains disabled. Production private-project creation remains disabled pending Phase 3 sandboxing; application policy does not provide process/filesystem isolation.

Validation: focused completion/reuse race tests, swarm/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Atomic completion-successor preparation

Reviewer/merger dispatch now uses `PrepareSwarmSuccessor` instead of separate swarm-field, category and status updates. Its write transaction reloads the parent, completed source and destination, checks project/parent/role scope and destination status/category, rejects active executions and stale duplicate callbacks, and validates the pinned identities and provenance. Readiness, configuration, board order, pending status, completion timestamp reset and `swarm.successor_prepared` audit commit together. Category/status events publish only after commit. Service submission still follows fresh source and destination authorization checks.

Tests cover reviewer, merger and legacy integrator roles, complete task-state rollback on audit failure, scope rejection, successful atomic preparation and stale duplicate rejection. The existing source/destination revocation tests now inject removal immediately before this transaction and verify no successor state change or submission. Existing legacy swarm regressions remain compatible.

This supersedes the partial-successor-write limitation above, not whole-swarm atomicity. Source completion records remain separate from authorizing future work. Manual multi-task reruns, planner generation transitions, post-commit submission/recovery and the remaining Phase 2 inventory require further verification. Authenticated Discord ingress remains disabled; production private-project creation remains disabled pending Phase 3 sandboxing. These application checks do not establish process/filesystem isolation.

Validation: atomic-preparation and callback revocation race tests, swarm/Discord/schedule/goal persistence/service/handler regressions, and server tests pass.


## Atomic manual reviewer/merger rerun preparation

Manual reviewer, merger and legacy integrator reruns now use one `PrepareSwarmRerun` transaction for parent generation/configuration, parent running/active state, child rerun configuration, child pending/active state, board order and completion timestamps. It rechecks both pinned identities and channel provenance, reloads relationship/project/role scope, rejects active child executions, and compares observed status, category, config and swarm fields to prevent a stale rerun overwriting a stop or generation change. The `swarm.rerun_prepared` audit commits with both task updates. Notifications are emitted after commit; cancellation flags and worker submission follow an additional authorization check.

Tests inject a failure during the child write (after the parent update) and during audit insertion, verifying both complete task records roll back. Coverage includes reviewer/merger/legacy-integrator roles, foreign scope, stale observations and duplicate requests. Service tests remove the child's binding after preflight but before the transaction, verifying neither task changes and no worker submission occurs.

This closes the partial parent/child write gap for manual role reruns, not all swarm orchestration. Planner generation transitions, multi-child plan application, recovery between commit and submission, downstream mutations and the full original Phase 2 inventory remain open. Authenticated Discord ingress remains disabled. Production private-project creation remains disabled pending Phase 3 sandboxing; policy tables do not provide process/filesystem isolation.

Validation: atomic rerun persistence race tests and late-revocation service race tests, swarm/Discord/schedule/goal persistence/service/handler regressions (including legacy behavior), and server tests pass.


## Atomic existing-planner follow-up generation

Parent follow-ups with an existing planner now use one `PreparePlannerFollowup` transaction for the parent generation/stop revision and running/active state, plus the planner prompt, model, configuration and pending/active state. The transaction rechecks pinned identities and channel provenance, relationship/project scope, observed task state and active executions, and commits `swarm.planner_followup_prepared` with both records. A pending planner without an active execution remains eligible. Existing chain configuration is preserved. Notifications follow commit, and service authorization is rechecked before clearing cancellation flags or submitting work.

Tests cover whole-record rollback when the planner update fails after the parent write and when audit insertion fails, stale/foreign observations, duplicate requests, prompt persistence and late binding removal between service preflight and persistence. Existing follow-up/chain-config regressions remain part of verification.

This covers existing-planner follow-ups only. Follow-ups that must create their first planner, multi-child plan application, callback recovery and the broader original Phase 2 inventory remain open. The first-planner path now checks the parent's saved identity before changing state, but its multiple writes are not yet atomic. Authenticated Discord ingress stays disabled. Production private-project creation stays disabled pending Phase 3 sandboxing; policy tables do not establish process/filesystem isolation.

Validation: atomic planner-follow-up persistence and service revocation race tests, swarm/Discord/schedule/goal persistence/service/handler regressions, and server tests pass after clearing old regenerable Go cache files to resolve disk exhaustion.


## Atomic ordinary first-planner creation

Ordinary manual startup with no existing planner now pins the observed parent and defers its active/blocked transition into child creation's transaction. Persistence rechecks the pinned parent identity and provenance, current project/relationship/role/state, and absence of another planner. Parent board state and `swarm.first_planner_prepared` audit commit with the child, run-as/provenance records, optional goal and metadata callback. Title-conflict retries roll back the parent with the failed child attempt. Parent notifications and cancellation-flag clearing occur only after successful persistence.

Tests cover binding removal before admission, child-insert failure after parent preparation, audit failure, metadata-callback failure, unchanged complete parent state/no orphan planner, successful child and goal identity inheritance, stale retry rejection and later child identity revocation. Existing manual planner and legacy swarm regressions are included in verification.

This closes ordinary first-planner creation only. Active-lane guarded startup, follow-ups that advance a generation before creating their first planner, multi-child plan application and remaining Phase 2 surfaces remain open. Authenticated Discord ingress stays disabled. Production private-project creation stays disabled pending Phase 3 sandboxing; application policy does not establish process/filesystem isolation.

Validation: first-planner persistence/manual-planner race tests, ordinary-task rejection route race tests, swarm/Discord/schedule/goal persistence/service/handler regressions, and server tests pass. The prior route contract allowing an ordinary task to start a planner was corrected: rejection preserves backlog state and creates no planner.
