# Automation Graphs User Guide

Use `/automations` to build project-scoped workflows from supported OpenVibely capabilities and watch their current state in one visual graph.

## What Automation Graphs Are For

Automation Graphs connect schedules, Tasks and Agents, Native approvals, supported GitHub actions, and outcomes through explicit handoffs. They reuse the same Tasks, Scheduler, Alerts, GitHub integrations, workers, and queues used elsewhere in OpenVibely.

Use Automation Graphs when work needs multiple supported steps and visible handoffs. Use [Schedule](./schedule-user-guide.md) when one Task simply needs to run once or repeat.

Existing Tasks, schedules, Alerts, and GitHub objects never become Automations automatically.

## Create an Automation

Open `/automations`, select `+ New Automation`, and choose how to start:

| Starting Point | Use It For |
|---|---|
| Template | Open a modal, choose a maintained Native or GitHub SDLC graph, and review the selected template's description before loading it. |
| Describe | Open a modal, describe the result you want, and review a generated browser-local graph. |
| Custom | Open the custom builder on an empty canvas. |

Templates provide supported starting points. Selecting `Use template` loads the maintained graph into the browser-local builder without creating an Automation, Task, or schedule. Review or customize it, then select `Save changes`; Template, Custom, and Describe use the same builder and atomic Save rules. The GitHub SDLC template owns complete Offering Manager, finder, Dev Inbox, and Loop Auditor prompts plus their cadences; it works without the bootstrap skill and adds saved graph identity, lifecycle controls, and Live visualization over the Tasks and Schedules it creates on Save.

## Build the Graph

Select `Add node` to add a supported step:

- `Schedule` defines recurring or one-time work and creates its own Task and schedule when saved.
- `Task` defines project work, optionally performed by a selected Agent.
- `Create notification`, `Human approval`, `Approved inbox`, and Native `Implementation` model the Native Alert mailbox. Approved inbox is itself the scheduled Task; Native Implementation represents the real runtime-created implementation Task and is not a placeholder Task created on Save.
- `Create GitHub issue`, `Human assignment`, `GitHub inbox`, `Open pull request`, and `Human review` model supported GitHub handoffs.
- `Outcome` marks a supported terminal result.

Configure each node, then connect its right output to the next node's left input. The builder supports selecting, moving, deleting, and reconnecting nodes and edges, plus pan, zoom, Fit, and Reset controls. A new Custom canvas uses a bounded height and the builder remains vertically scrollable on constrained screens. On a saved Automation's Edit page, use the canvas card's top-right kebab for `Save changes` or `Delete`; the Automation name, assumptions, and warnings appear below the canvas. Schedule nodes do not have individual enable or disable controls: every current schedule runs while its Automation is Active, Pause disables all of its schedules, and Resume re-enables them.

Mailbox graphs must use one complete family. Native uses `Create notification → Human approval → Approved inbox → Implementation → Outcome`. GitHub uses `Create GitHub issue → Human assignment → GitHub inbox → Task → Open pull request → Human review → Outcome`. GitHub inbox is itself a substantive scheduled Task and owns its schedule; do not add a separate Schedule before it. Native and GitHub approval, inbox, implementation, and review stages cannot be mixed in one custom graph.

Save rejects unsupported or ambiguous handoffs, invalid connector directions, unsafe cycles, missing configuration, and project references that are unavailable or belong to another project. The builder does not expose the hidden Workflow subsystem or arbitrary executable nodes.

## Save Changes

New and edited web graphs exist only in browser memory until you select `Save changes`. Refreshing or navigating away discards unsaved changes.

Save validates the complete graph and immediately creates or replaces the Automation's one current saved graph and required resources. A replacement keeps the same Automation identity, removes runtime projection tied to the replaced graph, and does not retain selectable or restorable graph versions.

There is no persisted editable Automation draft, separate Publish step, Definition page, Automation history page, or failed-Save recovery item. Save writes the graph, Tasks, and schedules in one database transaction. If anything fails, the whole Save is rolled back: a first Save creates nothing, and an edited Automation keeps running its previous graph.

## Use the Live Graph

Open a saved Automation from `/automations` to see its full-width `Live` graph. Current waiting, running, blocked, failed, and completed state appears on the graph itself. Saved Live and Edit canvases use the same responsive viewport height, node geometry, typography, coordinates, padded graph bounds, and tight Fit behavior, so matching graphs retain the same visual node scale before and after selecting `Fit`, including one-node and small graphs. Their canvas height is capped at `42rem` and retains a `20rem` usability floor at every viewport width; short pages scroll vertically instead of collapsing or clipping the graph.

Schedule and Task nodes link to their exact project Task when one is bound. Other node types remain visual. Editing starts a new browser-local copy of the current graph; the saved graph continues to run until a replacement Save succeeds.

## Run Now, Pause, Resume, or Delete

The Live graph card keeps lifecycle status and health visible in its header. Open the top-right kebab menu for Edit automation, Run now, Pause or Resume, and Delete. Run now appears only while the Automation is active.

- Run now immediately queues every eligible schedule-owned entry Task through the normal Automation worker. The Live canvas refreshes in place and marks each newly claimed entry node as running before worker activity arrives; later activity and work-item state remain authoritative. Completed GitHub issue discovery waits at the Dev Inbox until implementation handoff instead of leaving the completed inbox poll displayed as running. Run now uses each Task's saved prompt and Agent configuration without changing the schedule's next run or recurrence. Entries that are already queued, running, or reserved are skipped.
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
