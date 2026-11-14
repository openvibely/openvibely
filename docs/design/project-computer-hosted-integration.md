# Project computer and computer-use investigation

## Scope and conclusion

Inspected the local `openvibely-hosted` checkout at commit `932c186` and this
OpenVibely task branch (starting at `224796241`), including its merge with local
`main`. This is a source investigation, not a verification of a deployed hosted
installation. No hosted files or infrastructure were changed.

The hosted checkout does **not** currently implement project computers or agent
computer use. It provisions OpenVibely workspaces. This OpenVibely task branch
already implements the computer runtime and application integration; those files
are absent from local OpenVibely `main`. The missing hosted integration is a
control-plane implementation matching the client already in this branch.

## What hosted actually does

`openvibely-hosted/ARCHITECTURE.md` describes one OpenVibely application container
and durable `/data` volume per user. Its `internal/provisioner/provisioner.go`
creates and checks workspace networks, containers, volumes, and ownership labels.
Traefik routes authorized browser requests to the workspace application.

`workspaceEnv` supplies `OPENVIBELY_HOSTED_INSTANCE_ID`,
`OPENVIBELY_HOSTED_CONTROL_URL`, and `OPENVIBELY_HOSTED_AGENT_TOKEN`.
It does not supply `OPENVIBELY_COMPUTER_CONTROL_URL`. No computer lifecycle
handlers, computer runtime, or computer persistence were found in the hosted
source. Browser automation under `preview/` is for preview testing.

`internal/httpapi/update_agent.go:authenticateWorkspaceAgent` already maps a
bearer token to a workspace instance, verifies the stored credential, and handles
rotation. This is an integration point for authentication, not an existing
computer API. Computer routes also need lifecycle/entitlement checks.

## What this OpenVibely branch already supplies

| Area | Existing implementation |
| --- | --- |
| Provisioning | `internal/service/project_computer_provisioner.go`: local Docker lifecycle and a hosted HTTP client behind `ComputerProvisioner` |
| Runtime | `cmd/computer/`, `packaging/computer/`: Linux desktop, Chromium/Playwright, shell/files, screenshots/input, noVNC/WebSocket viewing |
| Persistence | `internal/models/project_computer.go`, `internal/persistence/sqlite/project_computer*`: project settings, task sessions, grants, handoffs and recovery |
| Task execution | `internal/service/llm_service.go` composes computer runtime tools into the task tool set; `ProjectComputerService.RuntimeTools` binds them to the task |
| Agent tools | `internal/service/project_computer_tools.go`: `computer`, `computer_shell`, `computer_files`, `project_browser`, jobs, transfers, attachments and handoffs |
| Model images | `pkg/openai_client/computer_images.go` and `pkg/anthropic_client/agentic.go` translate screenshot results into model image content |
| UI and access | `web/templates/pages/project_computer.templ`, task templates, and `internal/handler/project_computer*`: viewer, files, human control, grants and access checks |
| Lifecycle | `internal/service/project_computer_idle.go`, reconciliation and recovery services; server starts idle maintenance |

A project has one container and durable home volume. Each task has its own Linux
namespaces, users, desktop, files and browser profile inside that container.
See [the isolation design](project-computer-isolation.md) for the implemented
boundary and runtime tests. Computer tools are ordinary runtime function tools
with screenshot results; they are not restricted to a provider-specific native
computer-use API. Actual model effectiveness still requires appropriate vision
and tool capabilities.

Local provisioning invokes Docker, binds the control port to host loopback,
builds the embedded image when no override is configured, and preserves the home
volume across stops/replacement. The current default limits are 2 CPUs, 4 GiB
memory, 1 GiB shared memory, and 1,024 PIDs per project. The trusted supervisor
requires `SYS_ADMIN`, `NET_ADMIN`, `/dev/net/tun`, and the configured AppArmor
exception; untrusted task processes drop capabilities.

## Existing hosted contract and immediate blocker

`defaultComputerProvisioner` selects hosted mode when either
`OPENVIBELY_HOSTED_INSTANCE_ID` or `OPENVIBELY_COMPUTER_CONTROL_URL` is set.
It reads the control URL from `OPENVIBELY_COMPUTER_CONTROL_URL` and authenticates
with `OPENVIBELY_HOSTED_AGENT_TOKEN`. It intentionally does not fall back to
local Docker in a hosted workspace.

The client appends these paths to the configured base URL:

| Method/path | Meaning | Successful response |
| --- | --- | --- |
| `GET /projects/{projectID}/computer` | Resolve a running computer | JSON `endpoint` and `token` |
| `POST /projects/{projectID}/computer` | Start/ensure a computer | JSON `endpoint` and `token` |
| `POST /projects/{projectID}/computer/stop` | Stop it, retaining data | Any 2xx response |

The connection is private backend state. The bridge endpoint must be reachable
from the workspace container, support authenticated HTTP and viewer WebSockets,
and return health capabilities `isolation: 1` and `viewer_connections: 1`.
The client rejects missing credentials and malformed connection responses.

With today's hosted environment injection, this branch selects hosted mode but
has an empty computer control URL. Starting a computer therefore fails with
“hosted project computer provisioning is not configured.” Setting that variable
alone is insufficient: the server routes and provisioning behind it do not exist
in the inspected hosted checkout.

## Recommended implementation

Keep the UI, task tools, authorization, takeover and desktop runtime in
OpenVibely. Add resource management to hosted using its existing Docker API and
workspace identity mechanisms.

1. **Hosted persistence and identity.** Store computer records keyed by
   `(instance_id, project_id)`, with container/volume identity, lifecycle state,
   image version and protected bridge credential. Derive the instance solely
   from the authenticated workspace-agent identity. Scope Docker names and
   ownership labels to both IDs; project IDs alone must not identify resources
   across workspaces. Make concurrent start/stop requests idempotent.
2. **Hosted lifecycle API.** Add the three existing contract routes through the
   hosted route registration/policy machinery and workspace-agent auth. Enforce
   workspace eligibility and resource quotas. Start the same computer image and
   verify health before returning its connection. Preserve volumes on stop and
   apply the runtime isolation configuration described above.
3. **Networking and configuration.** Inject `OPENVIBELY_COMPUTER_CONTROL_URL`
   into workspaces. Return a private bridge endpoint reachable by its owning
   workspace; host `127.0.0.1` is not reachable as host loopback from an ordinary
   workspace container. Reuse the per-workspace network where appropriate, but
   ensure task workloads cannot reach control-plane or other workspace services.
   Keep bridge tokens server-side and public viewers behind OpenVibely's existing
   authenticated proxy. Do not give application workspaces the Docker socket.
4. **Complete lifecycle semantics.** Stop computers on workspace suspension and
   include them in deletion/reconciliation. Define backup/restore for the separate
   computer volumes; existing workspace `/data` backup coverage must not be
   assumed to include them. Add a project-removal contract: the current hosted
   provisioner implements stop but not the optional `RemoveProject` hook.
5. **Close contract differences.** Local provisioning supports
   `StartWithTimeZone`, while the current hosted client sends a bodyless start
   request. Extend the contract to carry the configured time zone. Specify
   image/capability upgrades, resource limits, start timeouts, credential rotation,
   and errors for unavailable provisioning. Expose an actionable unavailable
   state in the application when hosted computer provisioning is not configured.
6. **Validate the full boundary.** Test two workspace identities using the same
   project ID, concurrent starts, token rotation, forbidden resource access,
   suspension/deletion, preserved state after restart, viewer proxying, takeover,
   task isolation and screenshot delivery to model clients. Run the existing
   Docker isolation and lifecycle tests against the actual deployed image.

For local OpenVibely, integrate and validate the existing branch rather than
reimplementing computer use. For hosted OpenVibely, implement the server side of
this contract before advertising the feature as available.

## Dead-code audit

Ran Go `deadcode` v0.52.0 from the server entry point and separately with Linux
build settings from `cmd/computer`. Compared server findings against lines added
by this branch relative to local `main`: none of the reported unreachable
functions were introduced by the computer implementation. The Linux computer
entry point reported no unreachable functions. A supplemental reference scan of
computer Go functions and named functions in the viewer template and packaged
JavaScript helpers found no unreferenced declarations.

A second manual pass corrected the initial conclusion: function reachability
and internal JavaScript references missed an unlaunched script, an unreachable
shell branch, and repository wrappers retained solely for tests. Removed:

- `packaging/computer/auth-sync.cjs` and its Dockerfile copy instruction. Nothing
  launched this legacy project-wide browser-auth synchronization helper.
- The supervisor shutdown call inside the desktop shell shutdown function. The
  supervisor path already exits via `exec` at the top of that script; the Go
  supervisor owns its authenticated shutdown request.
- Repository `RequestLogin` and `CompleteLogin` wrappers and their interface
  entries. Production uses `RequestHandoff` and `FinishHandoff`; tests now call
  those active methods directly. The service's distinct `CompleteLogin` method
  remains in use and was retained.

A subsequent caller audit also removed the unused secondary research Chromium
process, its `/public-cdp` proxy, health dependency, and URL rewriting. The only
caller was an isolation-test assertion; application browser operations use the
primary desktop browser. Removed an unreachable VNC proxy branch after its
non-VNC routes had already returned. Isolation checks retain coverage of the
primary browser's CDP proxy.

A later startup-path review removed the shell's obsolete shared-profile and
fallback-port setup, along with the unused port-zero case in `computerPort`.
The supervisor exits the shell via `exec`; task desktops receive a positive
port base and isolated profile. Nonpositive task bases are now rejected.

A field-level review removed `computerViewer.TaskID`, which was assigned but
never read. Viewer registration and lookup already scope entries by task ID in
the map key. Every remaining computer-store interface method has a production
caller.

Removed the obsolete `/vnc/` static viewer proxy, websockify's static-file
serving option, and the container's duplicate `novnc` package. The UI loads
OpenVibely's bundled noVNC module and uses the authenticated `/rfb-view` and
`/rfb-control` connections, which remain active.

Staticcheck U1000 checks of the relevant Go packages and Linux bridge found no
additional computer-specific unused declarations. This does not prove every
runtime branch is exercised. The hosted provisioner remains reachable through
configuration and has contract tests; its missing hosted server is an integration
gap. Server-only findings elsewhere can also be desktop-entry-point or test
helpers and were not removed.

## Merge compatibility

The task branch is rebased onto local `main`. Main's migrations through 232 are
unchanged. All unmerged computer schema changes are consolidated into
`233_project_computers.sql`, defining the final columns directly in six CREATE
TABLE statements. Its rollback drops child tables before their parents.

The migration regression test covers upgrade from main's version 232, rollback,
and re-upgrade while preserving an existing custom-tagged task. No existing
database was modified during this work.

## Validation performed

- `make templ` regenerated the merged templates; `go build ./cmd/server` passed.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/computer` passed
  (output was directed to `/tmp`). The Linux-only runtime cannot run as a native
  macOS Go test package.
- Full SQLite persistence/migration suites, LLM packages, OpenAI client and
  Anthropic client tests passed.
- `TestComputerMigrationsUpgradeFromMain232` passed, exercising upgrade,
  rollback, re-upgrade and preservation of an existing custom-tagged task.
- Computer/task-workspace tests in service, handler and page packages passed
  with `-short -run 'Computer|TaskWorkspace'`.
- The broader service, handler and page suites hit the 180-second package timeout
  while running `TestPersonalGitContextUsesPreparedRuntime`,
  `TestLiveWebSocketSlowConsumerIsDisconnected`, and
  `TestBrowserFunctional_CardMenuOpeningClickSurvivesReleasePaint`, respectively.
  Those complete suites are not validated by this run. Docker integration and
  runtime isolation tests were not executed; no live hosted rollout was tested.
