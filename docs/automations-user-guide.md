# Automation Graphs User Guide

Use `/automations` to author project-scoped workflows in YAML and view their current state as a graph. Automation Graphs reuse the existing Tasks, Scheduler, Alerts, GitHub integrations, workers, queues, validation, lifecycle, and approval boundaries. They do not run arbitrary code or introduce another executor.

## Create an Automation

Open `/automations`, select `+ New Automation`, and choose Template, Describe, or Custom. Each option opens browser-local YAML. Templates load the maintained Native SDLC or GitHub SDLC YAML without creating an Automation, Task, schedule, graph row, or runtime resource. Describe creates a supported candidate, then renders it as YAML. Refreshing or navigating away discards unsaved YAML.

The YAML document is the complete editable Automation definition. Graph mode remains interactive for topology and layout: add or delete nodes, connect or reconnect nodes, and move nodes. Those canvas changes update YAML immediately. Node and connection configuration, including prompts, schedules, agents, tools, skills, labels, and conditions, is edited only in YAML; there are no node or connector settings cards. After manually editing YAML, select Graph or Preview to parse it and rebuild the interactive canvas. Saved Edit uses the same behavior. Existing Automations are read from their current stored graph and serialized to YAML when opened; this does not rewrite the record, prompts, configuration, or runtime resources.

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

Node types, roles, and configuration fields are constrained by the selected adapter. Task and schedule configuration supports prompts, optional goals, categories, priority, agent references, skills and source files where the selected node supports them, and recurrence settings. Maintained Native and GitHub SDLC templates are stored as canonical YAML and decoded through this same boundary when selected. Retained template nodes preserve their specialized runtime behavior; custom nodes use the existing Custom Automation capability rules. `position` is optional: omitted positions receive a deterministic preview layout during normalization. Moving a node in Graph mode writes its position to YAML; all other configuration remains YAML-only.

## Validation and Save

YAML is decoded, normalized, validated, and compiled through the same path used by Preview and Save. Select Preview to parse the current browser-local document and return to Graph mode without persisting or creating resources; Save performs that same validation before the atomic compiler transaction. It fails closed on malformed YAML, duplicate keys, aliases, unknown document fields, unsupported values, unsupported configuration, invalid references, invalid ports, duplicate keys, dangling endpoints, cycles, invalid topology, unavailable project capabilities, and all existing safety checks. Parse and validation errors are shown in the editor. They create no resources and leave the current saved graph and resources untouched.

Save atomically creates or replaces the one current graph and its owned Task/Schedule bindings. The saved graph remains a point-in-time prompt/configuration snapshot. Reopening, template registration, and YAML serialization never silently upgrade prompts or recreate runtime resources. Editing a compatible Automation retains its stable Automation identity and the existing ownership/provenance rules for pending Native and GitHub work.

## Preview and Live

On a saved Automation, Graph shows the current rendered graph state. YAML shows the same current saved definition in a read-only view. Neither Preview nor selecting either view creates resources. Edit opens a browser-local YAML copy; the existing saved graph continues to run until an atomic Save succeeds.

## Human Review Boundaries

Native approval is completed by a person in Alerts. GitHub assignment, pull-request review, merge, release, and deployment remain human-controlled. A YAML document cannot grant itself approval or bypass graph validation, provenance, lifecycle, authorization, resource ownership, or approval boundaries.

## Related Guides

- [Tasks User Guide](./tasks-user-guide.md)
- [Agents User Guide](./agents-user-guide.md)
- [Schedule User Guide](./schedule-user-guide.md)
- [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)
- [OpenVibely-Native Autonomous SDLC User Guide](./openvibely-native-autonomous-sdlc-user-guide.md)
