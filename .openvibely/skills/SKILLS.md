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
## openvibely_release_workflow

[OpenVibely Release Workflow](openvibely_release_workflow/SKILL.md) — Automate the OpenVibely release process — preflight, artifact builds, AI-synthesized release notes, docs updates, and GitHub release publishing — for a given semver version.
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

[OpenVibely GitHub PR Publication Workflow](openvibely_github_pr_publication_workflow/SKILL.md) — Implement, investigate, and audit live GitHub PR branch publication, PR reuse, and guarded history cleanup.
## openvibely_managed_memory_maintenance

[OpenVibely Managed Memory Maintenance](openvibely_managed_memory_maintenance/SKILL.md) — Apply narrow, idempotent corrections to authoritative OpenVibely managed-memory topics and verify the saved result.

## openvibely_security_boundary_workflow

[OpenVibely Security Boundary Workflow](openvibely_security_boundary_workflow/SKILL.md) — Implement and audit strict configuration, HTTP protocol, retry-deadline, cookie, identity, and untrusted-content boundaries for OpenVibely authentication and external integrations.

## openvibely_github_actions_workflow

[OpenVibely GitHub Actions Workflow](openvibely_github_actions_workflow/SKILL.md) — Implement and validate secure GitHub Actions dependency caching and other workflow-only changes.

## openvibely_automation_graph_workflow

[OpenVibely Automation Graph Workflow](openvibely_automation_graph_workflow/SKILL.md) — Implement, debug, and explain OpenVibely Automation graph nodes, browser-local creation, atomic Save, runtime handoffs, and projections without creating a parallel executor.

## openvibely_sdlc_loop_audit

[OpenVibely SDLC Loop Audit](openvibely_sdlc_loop_audit/SKILL.md) — Reconcile GitHub assignments, issues, pull requests, tasks, and deterministic Automation loop health without creating duplicate work.

## openvibely_alert_lifecycle_workflow

[OpenVibely Alert Lifecycle Workflow](openvibely_alert_lifecycle_workflow/SKILL.md) — Implement, audit, and diagnose OpenVibely alert lifecycle behavior and project-scoped runtime authorization.

## openvibely_model_release_audit_workflow

[OpenVibely Model Release Audit Workflow](openvibely_model_release_audit_workflow/SKILL.md) — Audit authoritative provider releases against OpenVibely support and hand off only verified model-support gaps.

## openvibely_github_implementation_inbox_workflow

[OpenVibely GitHub Implementation Inbox Workflow](openvibely_github_implementation_inbox_workflow/SKILL.md) — Poll GitHub implementation mailboxes, reconcile assigned issues with OpenVibely tasks, and start approved work without duplicate submissions.
## openvibely_docker_image_workflow

[OpenVibely Docker Image Workflow](openvibely_docker_image_workflow/SKILL.md) — Safely change OpenVibely production and coding-agent images while keeping consumers, release publishing, documentation, runtime validation, and sandbox boundaries aligned.
## openvibely_update_lifecycle_workflow

[OpenVibely Update Lifecycle Workflow](openvibely_update_lifecycle_workflow/SKILL.md) — Implement, audit, and explain OpenVibely update staging, draining, admission, restart validation, rollback, and durable recovery.

## openvibely_lifecycle_managed_artifact_boundary

[OpenVibely Lifecycle-Managed Artifact Boundary](openvibely_lifecycle_managed_artifact_boundary/SKILL.md) — Keep ordinary implementation and audit turns from editing lifecycle-managed skills or memories, and enforce explicit audit scope exclusions.

## openvibely_task_category_transition_workflow

[OpenVibely Task Category Transition Workflow](openvibely_task_category_transition_workflow/SKILL.md) — Implement and audit OpenVibely task category changes without bypassing completion, ordering, activation, cancellation, or goal lifecycle behavior.
## openvibely_optimization_finder_workflow

[OpenVibely Optimization Finder Workflow](openvibely_optimization_finder_workflow/SKILL.md) — Perform focused, evidence-backed, read-only OpenVibely performance audits and publish actionable GitHub performance issues.

## openvibely_github_suggestion_discovery

[OpenVibely GitHub Suggestion Discovery](openvibely_github_suggestion_discovery/SKILL.md) — Review OpenVibely vision and source for small feature gaps, then publish reviewable GitHub suggestion issues.

## openvibely_runtime_tool_input_decoding

[OpenVibely Runtime Tool Input Decoding](openvibely_runtime_tool_input_decoding/SKILL.md) — Keep runtime-tool JSON input normalization and error contracts consistent across OpenVibely surfaces.

## openvibely_protocol_serialization_audit

[OpenVibely Protocol Serialization Audit](openvibely_protocol_serialization_audit/SKILL.md) — Audit OpenVibely protocol serialization boundaries for wire-format limits that helper tests can miss.

## openvibely_query_projection_workflow

[OpenVibely Query Projection Workflow](openvibely_query_projection_workflow/SKILL.md) — Optimize summary/list/background-scan database reads with compact private projections without changing response contracts.

## openvibely_redundancy_finder_workflow

[OpenVibely Redundancy Finder Workflow](openvibely_redundancy_finder_workflow/SKILL.md) — Perform focused, read-only OpenVibely duplication audits and publish actionable GitHub redundancy issues.

## openvibely_bug_finder_workflow

[OpenVibely Bug Finder Workflow](openvibely_bug_finder_workflow/SKILL.md) — Perform focused, evidence-backed, read-only OpenVibely correctness audits and publish actionable GitHub bug issues.

## openvibely_theme_system_workflow

[OpenVibely Theme System Workflow](openvibely_theme_system_workflow/SKILL.md) — Implement and audit OpenVibely built-in theme catalogs, runtime theme selection, and theme-aware code styling.

## openvibely_project_form_source_workflow

[OpenVibely Project Form Source Workflow](openvibely_project_form_source_workflow/SKILL.md) — Keep OpenVibely project create/update form repository-source parsing, validation, clone/reclone, and response behavior aligned.

## openvibely_chat_link_opening_workflow

[OpenVibely Chat Link Opening Workflow](openvibely_chat_link_opening_workflow/SKILL.md) — Implement and audit safe external-link behavior for OpenVibely Chat and task-thread rendered messages, including desktop external browser handling.

## openvibely_toast_notification_workflow

[OpenVibely Toast Notification Workflow](openvibely_toast_notification_workflow/SKILL.md) — Implement and audit OpenVibely toast notifications using the shared HTMX/SSE toast bridge without duplicate or misleading toasts.

## openvibely_ui_preference_persistence

[OpenVibely UI Preference Persistence](openvibely_ui_preference_persistence/SKILL.md) — Persist browser and desktop UI preferences without theme flashes, layout jumps, or split storage contracts.

## openvibely_sdlc_finder_deduplication_workflow

[OpenVibely SDLC Finder Deduplication Workflow](openvibely_sdlc_finder_deduplication_workflow/SKILL.md) — Keep GitHub and Native SDLC Automation finder loops from recreating already-reported work.

## openvibely_scoped_file_tools_workflow

[OpenVibely Scoped File Tools Workflow](openvibely_scoped_file_tools_workflow/SKILL.md) — Implement and audit OpenVibely scoped file runtime path resolution, permission checks, containment protections, and bounded preview performance.

## openvibely_task_create_edit_workflow

[OpenVibely Task Create/Edit Workflow](openvibely_task_create_edit_workflow/SKILL.md) — Implement and audit OpenVibely task creation, edit, runtime create_task, and duplicate-title behavior.

## openvibely_reflection_metrics_workflow

[OpenVibely Reflection Metrics Workflow](openvibely_reflection_metrics_workflow/SKILL.md) — Implement and audit OpenVibely Reflection produced-commit stats recording, DB-first aggregation, and git fallback safely.

## openvibely_model_configuration_workflow

[OpenVibely Model Configuration Workflow](openvibely_model_configuration_workflow/SKILL.md) — Implement and audit OpenVibely model configuration and default-model mutations across Chat and the Models page.
