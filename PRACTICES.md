# OpenVibely - Project Practices

High-level development practices for this repository.

## Scope

Use the three project guides for different purposes:
- `MEMORY.md`: architecture, feature behavior, and current project state
- `guardrails.md`: concrete pitfalls, bug-prevention rules, and "never do this" guidance
- `PRACTICES.md`: reusable, high-level ways of working

Keep `PRACTICES.md` free of feature-specific runbooks, endpoint-level behavior, and provider/model edge-case notes.

## Development Workflow

### 1. Read Context First
- Review `MEMORY.md`, `guardrails.md`, and `PRACTICES.md` before coding.
- Confirm where the change belongs in the layered architecture before editing.

### 2. Make Coherent Changes
- Prefer end-to-end slices through the proper layers (`models -> repository -> service -> handler -> templates`) instead of scattered one-off edits.
- Keep changes minimal, explicit, and easy to reason about.

### 3. Validate Once Per Task
- Make code changes first, then run the project validation command chain once at the end (see `guardrails.md` for exact command and limits).
- If it fails, fix and run once more.

### 4. Use Logs Intentionally
- Use `logs/openvibely.log` to verify behavior and diagnose failures.
- Log meaningful state transitions and error paths so failures are actionable.

## Architecture Practices

### Layered Responsibilities
- `models`: plain structs and domain rules
- `repository`: raw SQL access with context-aware calls
- `service`: orchestration and business logic
- `handler`: HTTP parsing/rendering and response shaping
- `templates`: server-rendered UI structure
- When introducing behavior modes (for example planning vs execution), propagate mode through typed request contracts and enforce behavior in provider/tool policy layers, not only in prompt text
- For chat action execution, prefer request-scoped runtime tool injection (contract/context + adapter tool wiring) over parsing textual action markers from assistant output
- For task-execution actions, prefer exact entity targeting (`task_id`/`title`) when the request is about one task; reserve tag/priority filters for explicit group execution requests
- For chained task defaults, preserve parent execution intent when config omits an override (for example inherit parent category on empty `child_category` rather than falling back to a non-executing category)
- For UI behavior that depends on both persisted status and live VCS state (for example merged vs unmerged worktree diffs), prefer resilient fallback logic that verifies git truth (ancestry/diff) instead of trusting a single DB status field during transition windows
- When tasks run in isolated worktrees, provide the model explicit path orientation in the system prompt (for example `You are operating in an isolated git worktree at <path>.`) while keeping runtime workdir enforcement as the source of truth
- For agentic code-edit tools, prefer Codex-style resilient matching over strict byte-for-byte snippets: try exact match first, then progressively relax whitespace/punctuation matching before failing, and keep duplicate-match errors explicit when replacement intent is ambiguous
- For turn-level agentic tool execution, parallelize read-only tool calls for throughput but keep mutating/unknown tool mixes serial, and always preserve model-call order when appending tool results back into the next turn context

### Data Access Conventions
- Use raw SQL and parameterized queries (`?` placeholders).
- Prefer `QueryRowContext` with `RETURNING` for inserts that need created row data.
- Propagate `context.Context` through repository and service boundaries.
- Enforce cross-row invariants (for example default-record selection rules) inside repository transactions so behavior stays correct even when handlers/UI bypass optional workflows.
- When adding source metadata fields (for example project repository URL), update model + repository CRUD mappings together so create/list/get/update stay symmetric.

## Frontend Practices (Templ + HTMX)

- Run `templ generate` after modifying `.templ` files.
- For shared UI primitive class renames (for example chat/thread loading indicators), update both template/component tests and handler HTML assertions in the same change to keep UI-contract tests aligned.
- For integration cards (like Channels providers), model explicit connection states in UI (`Not Configured`/`Not Connected`/`Connected`) and wire actions with HTMX refresh headers for partial reload safety.
- For integration surfaces, separate discovery from management: use an Add dialog/list for available providers and show full cards only for providers already added/configured.
- For integration settings forms, keep persisted runtime toggles (for example notification on/off) round-trippable in UI state so opening/saving a config modal does not unintentionally reset operator choices.
- For cross-page card kebab menus, keep destructive action semantics consistent (`Delete` copy + shared destructive class such as `text-error` + confirmation affordance) and cover them with UI-contract tests.
- For HTMX forms, use explicit `method="post"` and return the appropriate fragment/container for consistent UI refresh.
- When deprecating a feature, remove all UI entry points and route handlers in the same change, and add regression tests that assert both control absence and endpoint inaccessibility.
- When changing default landing routes, implement it as an explicit redirect (for example `/` -> `/chat`) and keep previous pages reachable on explicit paths (for example `/dashboard`) to avoid breaking deep links and existing navigation habits.
- When one table mixes global and scoped entity controls, include an explicit scope indicator (for example a `Scope` column/badge) and keep the global row visually distinct (pinned and highlighted) so editing intent is unambiguous.
- Keep client-side behavior deterministic (avoid duplicate listener registration and brittle swap assumptions).
- For global UI state flips like theme changes, prefer immediate state application over whole-tree transition choreography; broad wildcard transitions (`*`) can create large-page repaint lag.
- For global UI overlays (toasts, dropdowns, modals), validate stacking behavior against native dialog top-layer semantics instead of relying only on larger `z-index` values.
- For grouped dropdown actions with section headers, keep the actionable row layout structurally uniform (same leading slot/indicator footprint per row) so text alignment stays consistent across sections and themes.
- For dynamic DOM elements created in inline page scripts (for example chat task-result links/buttons), prefer stable semantic CSS primitives (`ov-*` classes) over long utility-class literals so typography/state tuning can be applied consistently and regression-tested from base styles.
- For links rendered from markdown/tool-output/transformed marker text, centralize color/state styling in shared link typography tokens/classes (for example `--ov-link-color`, `.chat-markdown a`, `.ov-task-result-link`) so chat/thread/tool-call surfaces stay visually consistent across themes.
- For shared cross-surface UI primitives (for example chat + thread tool-call cards), use semantic theme tokens for foreground/background states and avoid low-contrast alpha-only color tweaks in light mode.
- When matching cross-theme component hierarchy, keep wrapper-vs-inner emphasis consistent between themes (for example transparent outer tool wrapper with only inner IN/OUT blocks surfaced) rather than restyling one theme independently.
- For recurring semantic chips/badges (for example `Default`), define and reuse a shared style token/class instead of per-page color classes so visual semantics stay consistent across pages and themes.
- For multi-column boards, keep column/dropzone container constraints (padding, border, overflow, flex sizing) intentionally aligned unless visual differences are explicit; subtle class drift causes cross-column card width misalignment.
- For icon-only card actions (for example delete `X`), keep cross-theme resting-state parity and put visual emphasis in interaction states (hover/focus/active) instead of theme-specific default fills.
- For async button actions, use explicit in-progress state (`...ing` label + spinner), disable conflicting actions while requests are in flight, and always restore state in `finally`.
- When feature-flagging UI actions, pair template-level visibility gating with server-side enforcement for the same interaction path (for example request marker/source fields) to prevent hidden-action access via crafted requests.
- For task-action visibility that depends on task lifecycle + git state, avoid using a single metadata field in isolation; preserve recovery actions in failed states even when merge metadata suggests a prior merge.
- For async list/state refreshes, guard against out-of-order responses (request token/sequence checks) so older responses cannot overwrite newer user actions.
- For polling-driven HTMX morph updates, make post-swap DOM processing incremental and content-signature based; avoid full-container reprocessing on every poll when content is unchanged.
- For high-frequency streaming UI updates (for example SSE token/tool chunks), batch DOM re-renders with `requestAnimationFrame` and force a final flush on completion to avoid main-thread stalls that make updates appear stuck.
- For streaming text sanitizers/parsers that hold a trailing lookahead buffer, flush only at UTF-8 rune boundaries (not arbitrary byte offsets) so multi-byte punctuation/emoji are not split across SSE chunks.
- For dirty-input preservation during polling, treat successful submit/update as an explicit state transition: temporarily suppress dirty-state restoration around the request lifecycle so accepted values do not bounce back into warning/edited styling.
- For polled chat/thread forms, scope polling to genuinely active states (for example `running`/`queued`), and persist unsent drafts by entity key so periodic swaps cannot erase in-progress user input.
- For draft-preserving HTMX forms, treat successful non-empty submit swaps as an explicit "clear" transition in `beforeSwap`/`afterRequest` handling, so restore logic does not rehydrate text that was just intentionally submitted.
- For request-lifecycle UI behavior (scroll resets, draft clears) on HTMX forms, detect the submit path by both trigger element and request URL when possible; trigger identity can vary between keyboard submit, button submit, and programmatic `requestSubmit()` even when user intent is the same.
- For pages that use both HTMX polling and HTMX form submit on the same route prefix, include HTTP method in request classification. Do not let `GET` polling paths trigger submit-only cleanup (for example draft clear/reset) that should run only on `POST` sends.
- For inline scripts inside HTMX-swapped fragments, use a window-level one-time binding guard to prevent duplicate event listeners and accumulated stale behavior.
- For cross-page toast feedback from HTMX handlers, prefer app-scoped `HX-Trigger` events bridged centrally in the base layout over page-local listeners.
- For toast actions (for example “Open Models”), pass structured toast metadata (`link_url`, `link_text`) and let the shared toast renderer build click behavior; avoid passing inline HTML in toast messages.
- For HTMX actions that can end in non-terminal states (for example merge conflicts with a refreshed panel), emit explicit toast triggers for those outcomes instead of relying only on visual fragment updates.
- For local Git workflows that support `ff-only`, account for moving target branches between sequential task merges: automatically refresh/rebase stale task branches before fast-forward checks, and preserve hard conflict outcomes (with clear recovery messaging) when rebases truly conflict.
- For modal forms that perform remote integration steps (for example project GitHub clone/re-clone), prefer HTMX no-swap submits and report failures via `openvibelyToast` instead of raw error payload swaps.
- For HTMX-re-rendered pages that must rebind global listeners, explicitly remove old handler references before adding new ones, and shut down tab-scoped SSE connections on container swaps/navigation.
- For SSE lifecycle hygiene, pair global EventSource registration with explicit unregister-on-close in all completion/error paths so connection tracking reflects only active streams.
- When multiple surfaces need realtime updates (for example sidebar tasks + chat live + task diff snapshots), prefer one shared per-tab SSE connection with in-browser event fan-out over opening separate long-lived EventSources per page/feature.
- For heavyweight tab content (large diffs, logs, timelines), prefer lazy-loading on tab activation instead of server-rendering hidden tab bodies during initial page load.
- For heavyweight task-thread transcripts, prefer a placeholder + tab-activation fetch (`GET /tasks/:id/thread`) instead of embedding full chat bubbles in the base task-detail payload.
- For large structured payload viewers, prefer per-item deferred rendering controls (for example per-file `Load diff`) with explicit auto-load/on-demand/hard-cap envelopes so rendering cost scales predictably.
- For mode handoffs (for example plan -> execute), prefer explicit UI confirmation prompts with one-click state transition over implicit auto-switching, and persist the selected mode in both hidden form state and localStorage.
- When restoring persisted mode state inside HTMX-swapped fragments, re-run dependent UI derivations (for example plan-completion CTA visibility) immediately after mode restoration/change so transient default values during hydration do not leave stale hidden states. For reconnect/outerHTML swaps, use an explicit hydration marker (for example `data-hydrated`) and a persisted-state fallback in derived-state readers to avoid pre-hydration flicker.
- For derived CTA/UI state that should survive refocus/reconnect until a real semantic change happens, prefer a small explicit latch keyed to semantic completion events over pure DOM rescans; DOM can be transiently incomplete during reconnect hydration and should not clear user-visible completion affordances by itself.
- For mode-based content suppression in chat renderers, scope suppression to live/streaming renders and preserve persisted history re-renders (for example hard refresh) so historical content continuity is maintained.
- Scope page-specific HTMX/JS selectors to page-unique roots (for example `#chat-page-root`) instead of shared semantic attributes (for example `[data-project-id]`) that may appear on multiple pages; this prevents cross-page content swaps during reconnect/refocus flows.
- When a user action succeeds but background state refresh can carry unrelated warnings, refresh silently for that action and keep warnings scoped to the relevant surface (item/runtime status, alerts) instead of raising global failure toasts.
- For plugin install/uninstall flows, treat action-coupled feedback separately from background discovery/runtime refresh warnings; suppress unrelated global warning toasts during the success path and keep runtime issues visible inline/alerts.
- When a single UI action spans global state and scoped state (for example install globally, enable per-entity), return explicit partial-success fields so the UI can update immediately and present actionable retry paths for the scoped step.
- When inputs are normalized for backend operations (for example URL/repo shorthand), keep user-facing display values intact in API/UI mapping so rendered cards and labels preserve what users entered.
- For forms that combine AI generation with persisted fields, place generation controls next to the canonical persisted input instead of creating duplicate prompt inputs.
- For datetime-local controls where browser pickers are icon-triggered by default, wrap the input in a click-delegating interactive container and open via `showPicker()` with `focus()` fallback so click-anywhere behavior works while keyboard input/accessibility remain intact.
- When a workflow has both create and edit schedule forms, keep repeat-field UX/validation parity in both paths (same visibility rules, enabled/disabled state, required flags, and numeric bounds) instead of implementing form-specific behavior drift.
- For form defaults that are also runtime semantics (for example schedule repeat type), keep template default selection, client-side modal reset state, and server-side fallback default synchronized so UI defaults and persisted behavior cannot drift.
- When a form field references dynamic configured entities (for example model configs), render selectable options from backend state instead of static hardcoded lists, and keep submitted values aligned to canonical persisted IDs.
- For AI outputs expected as structured JSON, implement a recoverable parse pipeline (strip wrappers, attempt structured extraction, validate schema shape, then reprompt once for strict JSON repair) before falling back.
- For JSON generation helpers (for example agent prompt drafting), keep the model context text-only and runtime-agnostic: no tool definitions, no plugin runtime resolution, and no capability-catalog injection in the generation request.
- For OpenAI direct JSON-generation calls in no-tools mode, enforce no-tools at payload level (`tool_choice: "none"`) in addition to request flags so provider/client transport differences (especially OAuth streaming responses) cannot re-enable function-call behavior implicitly.
- For no-tools JSON-generation streaming parsers, do not reuse pseudo-tool marker sanitizers that rewrite tool-like snippets. Keep output passthrough/text-only so valid JSON objects are never transformed into `[Using tool: ...]` markers.
- When fallback still produces usable user-facing content, prefer non-alarming warning/info messaging over hard-error presentation while still exposing concise fallback reason metadata.
- For compact list UIs with per-item async controls, keep interaction affordances visually lightweight (toggles/icons when appropriate) while preserving explicit disabled/aria-busy states to prevent duplicate submissions.
- Keep primary item actions visually explicit as buttons (not text-like links/chips) so hierarchy and click intent remain clear, while preserving existing loading/disabled states during async operations.
- For icon-only controls, always provide accessible labeling (`aria-label`) and a tooltip/title so intent remains clear without visible button text.
- Prefer surfacing related per-item health/status inline in compact rows (for example status dots + tooltip) instead of adding separate summary panels when the same signal can be shown in-place without increasing row height significantly.
- When browser capability APIs cannot reliably provide required filesystem data (for example absolute paths), prefer a server-side native dialog bridge for local installs, implement OS-specific picker adapters for major desktop targets, and keep manual absolute-path entry as fallback.
- For filesystem path settings sourced from pickers, persist only validated absolute paths; treat handle/display names as UI labels, not canonical paths.
- When picker APIs can emit home-relative values (`~`), normalize them on the server to concrete absolute paths before persistence.
- For runtime-managed artifacts (for example plugin caches/marketplaces), prefer app-local storage with an explicit environment-variable override instead of hardcoding a vendor-specific home-directory path.
- Keep runtime/generated directories (for example `uploads/` and managed cloned `repos/`) gitignored and untracked; if tests need filesystem writes, point them at `t.TempDir()` to avoid polluting repo paths.
- For plugin marketplace/install state, prefer app-owned local operations over external vendor CLI state so discovery and install/uninstall remain deterministic across environments.
- For Swagger/OpenAPI maintenance, treat route coverage as test-enforced contract: keep a parity test between registered API routes and spec paths so annotation drift is caught immediately.
- When UI loader primitives are shared (e.g., `ov-loading-dots`), keep template markup and handler/template assertions synchronized to avoid cross-suite regressions from stale class names.
- For multi-phase loading UIs (for example thinking vs streaming), preserve intended per-phase styling and verify resume/reconnect class toggles with real Tailwind semantics (`hidden` vs `!hidden`) so indicators remain visible while work is still in progress.

## Testing Practices

- Every bug fix should include a test that reproduces the failure scenario.
- For streaming/persistence bugs, add at least one regression test at the persistence layer and one at the rendered handler/view layer to verify end-user continuity.
- For retryable streaming paths that may re-enter with the same execution identifier, ensure writer/state initialization is rehydrated from persisted output before appending new chunks so retries cannot regress transcript continuity.
- For task lifecycle transitions that can reactivate completed work (for example thread follow-ups), include regression coverage for state-transition-triggered realtime subscriptions (SSE/polling) so updates resume without requiring manual tab changes.
- Use `testutil.NewTestDB(t)` for DB-backed tests.
- Production baseline should not assume a default model config; in tests, use `testutil.NewTestDB(t)` (which backfills a default test agent when absent) or create one explicitly for non-testutil DB setups.
- Respect SQLite constraints in fixtures (valid FK/check values).
- Avoid `t.Parallel()` when sharing DB resources.

## Maintenance Practices

When updating project guidance:
- Add architecture/feature-state facts to `MEMORY.md`.
- Add pitfall-prevention rules to `guardrails.md`.
- Add only reusable, high-level development practices to `PRACTICES.md`.
- Remove stale entries to keep guidance concise and reliable.
- For GitHub SCM integrations, default to PAT-based auth for local/self-hosted OSS usability, and expose GitHub App auth as an explicit Advanced mode for centralized cloud deployments.
