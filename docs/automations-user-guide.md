# Automation Graphs User Guide

Use `/automations` to build project-scoped workflows and monitor their current state. Automation Graphs reuse the existing Tasks, Scheduler, Alerts, GitHub integrations, workers, queues, validation, lifecycle, and approval boundaries. They do not run arbitrary code or introduce another executor.

## Create an Automation

Open `/automations`, select `New Automation`, and choose a starting point.

| Starting Point | What It Does |
|---|---|
| `Template` | Starts from the maintained Native SDLC or GitHub SDLC topology. |
| `Describe` | Generates a browser-local design from a natural-language description for you to review. |
| `Custom` | Opens the builder with an empty custom graph. |

All three paths use the same builder and validation rules. Nothing becomes active until Save succeeds. Refreshing or navigating away discards unsaved edits.

## Build The Graph

The builder has three synchronized views.

| View | Purpose |
|---|---|
| `Graph` | Add, move, delete, and connect nodes on the visual canvas. |
| `Details` | Configure Automation metadata, node prompts and settings, and transition labels or conditions. |
| `YAML` | Edit the complete Automation definition directly. Switching back to Graph parses and validates the YAML before rebuilding the canvas. |

In Graph, select `Add node`, choose a supported capability, and drag from one node's right output handle to the next node's left input handle. Use Details or YAML to configure the selected node or connection.

| Node | Purpose |
|---|---|
| `Schedule` | Starts work once or on a recurring schedule. |
| `Task` | Defines project work, optionally assigned to an Agent. |
| `Create notification` | Creates a Native Alert notification when connected work runs. |
| `Human approval` | Waits for a person to approve or reject the notification in Alerts. |
| `Approved inbox` | Receives approved Native work and creates the next visible task. |
| `Native implementation` | Performs approved implementation work inside OpenVibely. |
| `Create GitHub issue` | Creates a supported GitHub issue handoff. |
| `Human assignment` | Waits for the issue to be assigned in GitHub. |
| `GitHub inbox` | Creates implementation work from the assigned issue. |
| `Open pull request` | Opens or reuses the implementation pull request. |
| `Human review` | Waits for the configured GitHub review state. |
| `Outcome` | Marks the successful terminal result. |

## YAML Schema

The canonical document has these fields:

```yaml
schema_version: 1
name: Daily review
description: Review one focused area each day.
automation_type: custom
adapter_key: custom
nodes:
  - key: review
    name: Review requests
    type: trigger
    role: fixed_schedule
    config:
      prompt: Review one request.
      goal: ""
      category: scheduled
      priority: 2
      run_at: "09:00"
      repeat_type: daily
      repeat_interval: 1
      enabled: true
      clear_context_on_start: true
    position: {x: 0, y: 0}
  - key: follow_up
    name: Follow up
    type: agent_task
    role: task
    config:
      prompt: Follow up on the review.
      goal: ""
      category: backlog
      priority: 2
    position: {x: 260, y: 0}
edges:
  - key: review_to_follow_up
    from: review
    to: follow_up
    from_port: right
    to_port: left
    label: ""
    condition: {}
```

Top-level metadata is `schema_version`, `name`, `description`, `automation_type`, `adapter_key`, `nodes`, `edges`, and optional `assumptions` and `warnings`. A node has `key`, `name`, `type`, `role`, `config`, and optional `position`. An edge has `key`, `from`, `to`, optional ports and label, and optional `condition`.

Node types, roles, and configuration fields are constrained by the selected adapter. Task and schedule configuration supports prompts, optional goals, categories, priority, agent references, skills and source files where the selected node supports them, and recurrence settings. Maintained Native and GitHub SDLC templates are stored as canonical YAML and decoded through this same boundary when selected. Retained template nodes preserve their specialized runtime behavior; custom nodes use the existing Custom Automation capability rules. `position` is optional: omitted positions receive a deterministic preview layout during normalization. Moving a node in Graph mode writes its position to YAML; configuration changes made in Details also update the same browser-local definition.

## Validation and Save

YAML is decoded, normalized, and validated through the compiler's non-persisting preview path before Graph renders the submitted browser-local document, and Save uses that same validation before its atomic compiler transaction. There is no separate Preview button. Switching from YAML to Graph parses the document and rebuilds the interactive canvas without persisting or creating resources. Validation includes project capabilities and agent selection as well as malformed YAML, duplicate keys, aliases, unknown document fields, unsupported values, unsupported configuration, invalid references, invalid ports, duplicate keys, dangling endpoints, cycles, invalid topology, and all existing safety checks. Parse and validation errors are shown in the editor. They create no resources and leave the current saved graph and resources untouched.

Save atomically creates or replaces the one current graph and its owned Task/Schedule bindings. If validation or resource creation fails, the transaction is rolled back and the previously saved graph remains active. The saved graph remains a point-in-time prompt/configuration snapshot. Reopening, template registration, and YAML serialization never silently upgrade prompts or recreate runtime resources. Editing a compatible Automation retains its stable Automation identity and the existing ownership/provenance rules for pending Native and GitHub work.

## Monitor And Run

On a saved Automation, Graph shows the current rendered state, Details shows the saved configuration, and YAML shows the same definition in a read-only view. Switching views creates no resources. Edit opens a browser-local copy; the existing saved graph continues to run until an atomic Save succeeds.

The live view refreshes while visible. Schedule and Task nodes link to their bound project tasks, and tracked GitHub pull request state is refreshed automatically when it becomes stale.

Select `Run` from a live Automation, or `Run now` from its portfolio menu, to queue a manual run without changing its saved schedule cadence.

## Disable, Enable, Or Delete

| Action | Effect |
|---|---|
| `Disable` | Prevents new scheduled admissions while preserving the saved graph. |
| `Enable` | Allows eligible disabled work to enter execution again. |
| `Delete` | Removes the Automation and Automation-owned trigger schedules when deletion is safe. Existing domain tasks remain. |

Editing a disabled Automation does not activate it. Saving preserves its current lifecycle state.

## Update A Maintained Template

Native SDLC and GitHub SDLC Automations show `Update to latest template` when a newer maintained template revision is available.

Updating replaces the current nodes, connections, prompts, and schedules with the latest template. The Automation name and lifecycle state are preserved, but template customizations are not merged. Review the confirmation before applying the update.

## Human Review Boundaries

Native approval is completed by a person in Alerts. GitHub assignment, pull-request review, merge, release, and deployment remain human-controlled. A YAML document cannot grant itself approval or bypass graph validation, provenance, lifecycle, authorization, resource ownership, or approval boundaries.

## Related Guides

- [Tasks User Guide](./tasks-user-guide.md)
- [Agents User Guide](./agents-user-guide.md)
- [Schedule User Guide](./schedule-user-guide.md)
- [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)
- [OpenVibely-Native Autonomous SDLC User Guide](./openvibely-native-autonomous-sdlc-user-guide.md)
