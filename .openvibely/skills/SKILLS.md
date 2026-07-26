---
always_use:
    - openvibely_project_guidance
---

# Standalone Skills

## openvibely_skill_lifecycle_workflow

[OpenVibely Skill Lifecycle Workflow](openvibely_skill_lifecycle_workflow/SKILL.md) — Preserve current OpenVibely standalone-skill, Skill Curator, scoped routing, and lifecycle UI conventions.

## openvibely_database_migration_workflow

[OpenVibely Database Migration Workflow](openvibely_database_migration_workflow/SKILL.md) — Manage OpenVibely goose schema migrations, consolidation, and validation safely.

## openvibely_skill_index_staleness

[OpenVibely Skill Index Staleness](openvibely_skill_index_staleness/SKILL.md) — Diagnose and regress stale skill or agent index entries after metadata patches, archives, or deletes.

## openvibely_validation_workflow

[OpenVibely Validation Workflow](openvibely_validation_workflow/SKILL.md) — Plan and run OpenVibely validation commands without wasting limited build/test attempts.

## openvibely_cancellation_workflow

[OpenVibely Cancellation Workflow](openvibely_cancellation_workflow/SKILL.md) — Audit and implement reliable cancellation for OpenVibely tasks, threads, Chat, tools, hooks, streaming providers, and fixed source fan-out.

## openvibely_htmx_templ_ui_workflow

[OpenVibely HTMX Templ UI Workflow](openvibely_htmx_templ_ui_workflow/SKILL.md) — Diagnose and fix stale OpenVibely HTMX/templ UI fragments, streaming DOM updates, and state-gated controls.

## openvibely_lost_changes_recovery_workflow

[OpenVibely Lost Changes Recovery Workflow](openvibely_lost_changes_recovery_workflow/SKILL.md) — Recover lost or reverted OpenVibely task changes safely while preserving unrelated current work.

## openvibely_git_worktree_rebase_workflow

[OpenVibely Git Worktree Rebase Workflow](openvibely_git_worktree_rebase_workflow/SKILL.md) — Safely rebase OpenVibely task worktree branches onto main and recover startup auto-merge conflicts without losing task changes.

## openvibely_chat_provider_test_workflow

[OpenVibely Chat Provider Test Workflow](openvibely_chat_provider_test_workflow/SKILL.md) — Test OpenVibely chat, memory recall, and provider-normalized requests without confusing prompt text with model-facing context.

## openvibely_provider_adapter_workflow

[OpenVibely Provider Adapter Workflow](openvibely_provider_adapter_workflow/SKILL.md) — Implement and audit OpenVibely provider adapters, normalized AgentRequest routing, compaction, provider-native tools, and runtime tool payloads.

## openvibely_docs_editing_workflow

[OpenVibely Docs Editing Workflow](openvibely_docs_editing_workflow/SKILL.md) — Edit OpenVibely README and product docs conservatively while preserving useful examples and validating links.

## openvibely_chat_thread_turn_workflow

[OpenVibely Chat And Thread Turn Workflow](openvibely_chat_thread_turn_workflow/SKILL.md) — Implement OpenVibely Chat/task-thread follow-up queuing and mid-stream steering without conflating the two behaviors.

## openvibely_audit_review_workflow

[OpenVibely Audit Review Workflow](openvibely_audit_review_workflow/SKILL.md) — Perform audit-only reviews of OpenVibely task changes without editing files, while verifying the correct worktree and implementation scope first.

## openvibely_task_goals_workflow

[OpenVibely Task Goals Workflow](openvibely_task_goals_workflow/SKILL.md) — Implement and review OpenVibely task goal persistence, tools, UI, and continuation behavior.

## openvibely_channel_integrations_workflow

[OpenVibely Channel Integrations Workflow](openvibely_channel_integrations_workflow/SKILL.md) — Implement and debug OpenVibely GitHub, Slack, Telegram, Discord, Email, and inbound webhook integrations with shared chat/task-thread behavior.

## openvibely_lifecycle_hook_workflow

[OpenVibely Lifecycle Hook Workflow](openvibely_lifecycle_hook_workflow/SKILL.md) — Implement and debug OpenVibely lifecycle hooks, lifecycle agents, hook output contracts, runtime tools, and hook prompt chaining.

## openvibely_go_maintenance_workflow

[OpenVibely Go Maintenance Workflow](openvibely_go_maintenance_workflow/SKILL.md) — Run scheduled OpenVibely Go toolchain and module dependency maintenance consistently.

## openvibely_model_usage_tracking_workflow

[OpenVibely Model Usage Tracking Workflow](openvibely_model_usage_tracking_workflow/SKILL.md) — Implement and audit OpenVibely model usage persistence, aggregation, provider capture, and Analytics UI.

## openvibely_recursive_self_improvement_bootstrap

[OpenVibely Recursive Self-Improvement Bootstrap](openvibely_recursive_self_improvement_bootstrap/SKILL.md) — Bootstrap a reviewable task-and-schedule loop that drives explicit project vision, specs, and defects toward completion.

## openvibely_project_guidance

[OpenVibely Project Guidance](openvibely_project_guidance/SKILL.md) — Static coding-agent guidance for working in the OpenVibely repository.

## openvibely_followup_route_task_routing

[OpenVibely Follow-Up Route Task Routing](openvibely_followup_route_task_routing/SKILL.md) — Ensure task-thread follow-up lifecycle routing uses the current user turn for skill and memory selection.

## openvibely_backlog_task_management

[OpenVibely Backlog Task Management](openvibely_backlog_task_management/SKILL.md) — Create or update OpenVibely backlog tasks without accidentally starting them.

## openvibely_release_workflow

[OpenVibely Release Workflow](openvibely_release_workflow/SKILL.md) — Automate the OpenVibely release process — preflight, artifact builds, AI-synthesized release notes, docs updates, and GitHub release publishing — for a given semver version.

## openvibely_local_resource_diagnostics

[OpenVibely Local Resource Diagnostics](openvibely_local_resource_diagnostics/SKILL.md) — Inspect local macOS CPU, memory, and fan-pressure causes without disrupting OpenVibely or system processes.

## openvibely_worktree_merge_lineage_workflow

[OpenVibely Worktree Merge And Lineage Workflow](openvibely_worktree_merge_lineage_workflow/SKILL.md) — Implement and audit OpenVibely task worktrees, merge actions, Changes tab recovery, cleanup, and chained-task lineage.

## openvibely_test_coverage_audit_workflow

[OpenVibely Test Coverage Audit Workflow](openvibely_test_coverage_audit_workflow/SKILL.md) — Audit OpenVibely test count, coverage gaps, and CPU-heavy test execution with repeatable Go commands.

## openvibely_worker_concurrency_workflow

[OpenVibely Worker Concurrency Workflow](openvibely_worker_concurrency_workflow/SKILL.md) — Audit and fix OpenVibely worker queue slots, capacity counters, task claiming, and dispatch cleanup.

## openvibely_startup_seed_workflow

[OpenVibely Startup Seeding Workflow](openvibely_startup_seed_workflow/SKILL.md) — Implement and audit fresh-database startup seeding for protected agents, default projects, and scheduled system tasks.

## openvibely_scheduled_tasks_workflow

[OpenVibely Scheduled Tasks Workflow](openvibely_scheduled_tasks_workflow/SKILL.md) — Implement and audit scheduled task behavior, enabled state, next-run preservation, task-owned assignment, and schedule UI consistently.

## openvibely_anthropic_oauth_model_workflow

[OpenVibely Anthropic OAuth Model Workflow](openvibely_anthropic_oauth_model_workflow/SKILL.md) — Verify and implement Anthropic model support through OpenVibely's OAuth/agentic request path.

## openvibely_dynamic_task_loop_workflow

[OpenVibely Dynamic Task Loop Workflow](openvibely_dynamic_task_loop_workflow/SKILL.md) — Implement and audit dynamic task loop wakeups, Loop Agent tooling, scheduler enqueue paths, and UI cancellation safely.

## openvibely_skill_analytics_workflow

[OpenVibely Skill Analytics Workflow](openvibely_skill_analytics_workflow/SKILL.md) — Implement and audit OpenVibely Skill Curator analytics telemetry, aggregations, and dashboard surfaces.
## openvibely_spec_runbook_workflow

[OpenVibely Spec And Runbook Workflow](openvibely_spec_runbook_workflow/SKILL.md) — Resolve and follow OpenVibely source-of-truth specs and sibling runbooks before implementation.

## openvibely_skill_import_workflow

[OpenVibely Skill Import Workflow](openvibely_skill_import_workflow/SKILL.md) — Implement and audit OpenVibely skill package import across runtime tools, UI upload, YAML normalization, grants, and catalog indexing.

## openvibely_tool_output_rendering_workflow

[OpenVibely Tool Output Rendering Workflow](openvibely_tool_output_rendering_workflow/SKILL.md) — Diagnose and fix OpenVibely task/chat tool-result output persistence, rendering, scrolling, and streaming behavior.

## openvibely_mobile_dropdown_positioning

[OpenVibely Mobile Dropdown Positioning](openvibely_mobile_dropdown_positioning/SKILL.md) — Diagnose and fix mobile dropdown/popover positioning in OpenVibely scrollable templ/HTMX UI.

## openvibely_responsive_templ_layout_workflow

[OpenVibely Responsive Templ Layout Workflow](openvibely_responsive_templ_layout_workflow/SKILL.md) — Diagnose and fix responsive Tailwind/DaisyUI layout issues in OpenVibely templ UI without relying on screenshots.

## openvibely_agent_tool_surface_workflow

[OpenVibely Agent Tool Surface Workflow](openvibely_agent_tool_surface_workflow/SKILL.md) — Keep OpenVibely agent allowed-tool UI, validation, and runtime tool catalogs aligned when adding callable tools.

## openvibely_outbound_message_delivery_workflow

[OpenVibely Outbound Message Delivery Workflow](openvibely_outbound_message_delivery_workflow/SKILL.md) — Complete user-requested email or outbound-message delivery tasks without unnecessary confirmation and without falsely claiming sends.

## openvibely_agent_management_workflow

[OpenVibely Agent Management Workflow](openvibely_agent_management_workflow/SKILL.md) — Implement and audit OpenVibely Agents page CRUD, filesystem-backed agent declarations, and advanced agent settings persistence.

## openvibely_swarm_task_workflow

[OpenVibely Swarm Task Workflow](openvibely_swarm_task_workflow/SKILL.md) — Implement and audit OpenVibely agent/task swarm orchestration across task persistence, workers, tools, UI, and docs.

## openvibely_virtual_model_provider_workflow

[OpenVibely Virtual Model Provider Workflow](openvibely_virtual_model_provider_workflow/SKILL.md) — Implement OpenVibely virtual model providers that orchestrate other configured models without adding external credentials.

## openvibely_attachment_lifecycle_workflow

[OpenVibely Attachment Lifecycle Workflow](openvibely_attachment_lifecycle_workflow/SKILL.md) — Implement and audit durable OpenVibely Chat, task-thread, and task attachment publication, rollback, cleanup, and execution loading.

## openvibely_github_pr_publication_workflow

[OpenVibely GitHub PR Publication Workflow](openvibely_github_pr_publication_workflow/SKILL.md) — Implement and investigate idempotent API-backed task branch publication, PR reuse, and guarded history cleanup.

## openvibely_github_finding_workflow

[OpenVibely GitHub Finding Workflow](openvibely_github_finding_workflow/SKILL.md) — Run focused read-only OpenVibely repository scans and publish actionable findings as GitHub issues.

## openvibely_managed_memory_maintenance

[OpenVibely Managed Memory Maintenance](openvibely_managed_memory_maintenance/SKILL.md) — Apply narrow, idempotent corrections to authoritative OpenVibely managed-memory topics and verify the saved result.

## openvibely_security_boundary_workflow

[OpenVibely Security Boundary Workflow](openvibely_security_boundary_workflow/SKILL.md) — Implement and audit strict configuration, HTTP protocol, retry-deadline, cookie, identity, and untrusted-content boundaries for OpenVibely authentication and external integrations.

## openvibely_github_actions_workflow

[OpenVibely GitHub Actions Workflow](openvibely_github_actions_workflow/SKILL.md) — Implement and validate secure GitHub Actions dependency caching and other workflow-only changes.

## openvibely_automation_graph_workflow

[OpenVibely Automation Graph Workflow](openvibely_automation_graph_workflow/SKILL.md) — Implement, debug, and explain OpenVibely Automation graph nodes, browser-local creation, atomic Save, runtime handoffs, and projections without creating a parallel executor.
