# Automation Graphs User Guide

Use `/automations` to build project-scoped workflows from supported OpenVibely capabilities and watch their current state in one visual graph.

## What Automation Graphs Are For

Automation Graphs connect schedules, Tasks and Agents, Native approvals, supported GitHub actions, and outcomes through explicit handoffs. They reuse the same Tasks, Scheduler, Alerts, GitHub integrations, workers, and queues used elsewhere in OpenVibely.

Use Automation Graphs when work needs multiple supported steps and visible handoffs. Use [Schedule](./schedule-user-guide.md) when one Task simply needs to run once or repeat.

Existing Tasks, schedules, Alerts, and GitHub objects never become Automations automatically.

## Create an Automation

Open `/automations`, select `New Automation`, and choose how to start:

| Starting Point | Use It For |
|---|---|
| Template | Start from a maintained Native, GitHub, or Vision Driver graph. |
| Describe It | Describe the result you want and review a generated browser-local graph. |
| Blank | Build a custom graph from an empty canvas. |

Templates provide supported starting points, but Blank and Describe It use the same configurable graph builder and Save rules. The GitHub SDLC template reads the Offering Manager, finder, Dev Inbox, and Loop Auditor prompt patterns directly from the GitHub autonomous SDLC bootstrap skill shipped with OpenVibely; the Automation adds saved graph identity, lifecycle controls, and Live visualization over those Tasks and Schedules without changing the skill.

## Build the Graph

Select `Add node` to add a supported step:

- `Schedule` defines recurring or one-time work and creates its own Task and schedule when saved.
- `Task` defines project work, optionally performed by a selected Agent.
- `Create notification` creates a Native Alert notification at runtime.
- `Human approval` waits for a person to approve or reject the Native notification.
- `Create GitHub issue`, `Human assignment`, `GitHub inbox`, `Open pull request`, and `Human review` model supported GitHub handoffs.
- `Outcome` marks a supported terminal result.

Configure each node, then connect its right output to the next node's left input. The builder supports selecting, moving, deleting, and reconnecting nodes and edges, plus pan, zoom, Fit, and Reset controls.

Save rejects unsupported or ambiguous handoffs, invalid connector directions, unsafe cycles, missing configuration, and project references that are unavailable or belong to another project. The builder does not expose the hidden Workflow subsystem or arbitrary executable nodes.

## Save Changes

New and edited web graphs exist only in browser memory until you select `Save changes`. Refreshing or navigating away discards unsaved changes.

Save validates the complete graph and immediately creates or replaces the Automation's one current saved graph and required resources. A replacement keeps the same Automation identity, removes runtime projection tied to the replaced graph, and does not retain selectable or restorable graph versions.

There is no persisted editable Automation draft, separate Publish step, Definition page, or Automation history page. If a Save fails after resource application has started, `/automations` shows `Save needs attention` so you can reopen and retry the exact graph safely.

## Use the Live Graph

Open a saved Automation from `/automations` to see its full-width `Live` graph. Current waiting, running, blocked, failed, and completed state appears on the graph itself.

Schedule and Task nodes link to their exact project Task when one is bound. Other node types remain visual. Editing starts a new browser-local copy of the current graph; the saved graph continues to run until a replacement Save succeeds.

## Pause, Resume, or Delete

- Pause prevents new scheduled admissions while preserving the current saved graph.
- Resume re-enables eligible paused work.
- Delete removes the Automation and exclusively owned schedules when no Automation-owned work prevents safe deletion. Domain Tasks are preserved.

Saving edits preserves the current lifecycle state. Editing a paused Automation does not activate it.

## Human Review Boundaries

Native approval must be completed by a person in Alerts. GitHub assignment, pull request review, and merge remain human-controlled in GitHub. An Automation can observe these decisions and continue through a configured handoff, but it cannot grant itself approval or merge, release, or deployment authority.

GitHub nodes require the selected project to have a usable repository, provider authentication, and an enabled authorized-user inbox where the selected topology needs them.

## Create an Automation from Chat

Chat can prepare the same supported graph contract. It first displays a plan and waits for explicit Save confirmation. Before that confirmation, no Automation, URL, graph identity, Task, or schedule is created.

After confirmation, Chat validates and saves the Automation through the same resource and safety boundaries as the web builder.

## Related Guides

- [Tasks User Guide](./tasks-user-guide.md)
- [Agents User Guide](./agents-user-guide.md)
- [Schedule User Guide](./schedule-user-guide.md)
- [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)
- [OpenVibely-Native Autonomous SDLC User Guide](./openvibely-native-autonomous-sdlc-user-guide.md)
