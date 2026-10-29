# Project access audit

This audit covers the current single-instance application after the backend-neutral persistence refactor. It checks whether a request scoped to project A can act on a resource in project B. It does **not** establish user/org authorization or filesystem isolation.

## Findings and fixes

| Area | Finding | Change |
|---|---|---|
| Task routes | Task detail/history/thread and swarm routes could look up a task ID without comparing its project to the request. Other task routes already had individual checks. | Shared middleware checks registered `/tasks/:taskId` and `/api/tasks/:id/` routes against explicit or selected project context before the handler. Uses compact task metadata. Pulse cancellation retains its existing checked path and toast response. Foreign task IDs return 404. |
| Review comments | PATCH/DELETE accepted a comment ID without checking project ownership. | New database-neutral `UpdateTextForProject` and `DeleteForProject` operations include ownership in the mutation itself. Foreign/missing comments return 404. Review UI supplies project context. |
| Webhook management | Detail and deletion were scoped, but update, secret rotation, and synthetic test execution used global ID lookups. | Those operations first load the endpoint using `GetByIDForProject`. Foreign/missing IDs return 404 before changes, secret generation, or task creation. Signed inbound webhook handling is unchanged. |
| Queued messages | Edit, edit-hold, cancellation, and project-chat steering could use an input ID without checking the request project. | Shared input middleware checks explicit/selected project context and nested task IDs. Project-chat steering rejects task-thread inputs. |
| Chat status polling | Execution and queued-input status accepted IDs without comparing the request project; completed output could be returned. | Checks explicit/selected project context for queued inputs and the task owning each execution, including promoted executions. |
| Project selection | An explicit unknown project ID silently fell back to another project. | Unknown explicit project IDs now return 404. Omitted/default selection retains its existing semantics. |

## Existing checks inspected

- Live transport is now WebSocket based, not the old SSE implementation referenced by the original plan. `liveSubscriptionAllowed` requires an existing project and checks the task behind execution subscriptions. Task/chat notifications use project subscriptions; file notifications use a verified task subscription.
- Execution detail reads use `GetByIDForProject`; nested execution fragments verify the execution belongs to the task.
- Attachment download/deletion and generated-file access have project-scoped lookups and existing foreign-project tests.
- Schedule mutations check the owning task/project. Batch task category changes preflight every task before mutation.
- Alert, insight, automation, channel-target, and project-agent handlers already have project-aware access paths. This review inspected representative paths, not every possible combination of their payloads.
- Runtime task resolution uses `ResolveTaskReference` with the run's project. GitHub PR tools and cancellation reject foreign task targets; explicit project assertions in runtime actions are checked against the run context.

## Regression coverage

`internal/handler/project_boundary_test.go` walks the registered task routes with a foreign task and tests review, webhook, queued-input, and chat-status boundaries. It includes matching-project positive controls and checks that rejected mutations leave data unchanged. Existing live transport and runtime-tool rejection tests are retained.

The broader handler tests also cover attachment cleanup, compact polling projections, Pulse cancellation responses, and normal review/webhook mutations. Fixtures for review/webhook mutations now supply their owning project, matching the UI. Existing tests expecting 400 for foreign task IDs now expect 404 where the shared guard applies.

## Validation results

- Full `internal/handler` suite passed after correcting the regressions caught by the first run.
- Full `internal/persistence/sqlite`, `internal/service`, and `internal/testutil/teststores` suites passed.
- Targeted API-chat and boundary tests passed again after switching chat-status ownership checks to compact task metadata.
- Existing WebSocket project-isolation and runtime task/PR rejection tests passed.
- Changed templates were regenerated with the repository's pinned templ version; `git diff --check` passed.

## Remaining limits and next work

1. Authentication is still instance-wide. Any authenticated user can select another project. The project ID is a consistency boundary, **not proof of permission**. Phase 1/2 must introduce a server-derived principal and membership checks.
2. Legacy unscoped task deep links, queued-input operations, and chat polling retain their behavior when neither explicit nor selected project context exists. They must be authorized against the resource's project once users exist. Selected-project settings are global today; they must not become a multi-user authorization source.
3. The route-walking test covers task route families, not every HTTP endpoint. Expand it to a resource-wide permission matrix when user/org policy exists. Several existing resource handlers still distinguish malformed/missing/foreign IDs with different response codes.
4. Global models, settings, integrations, and project enumeration remain instance-level resources until the org/credential phases.
5. Shell/file tools, provider tools, MCP, server git calls, secrets, and network access still need the separately planned sandbox. This audit adds no Landlock enforcement.
6. No hosted-server or adversarial Linux sandbox validation was performed. This audit does not certify private projects as isolated.
