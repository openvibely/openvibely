# OpenVibely User Guides

OpenVibely turns AI coding into a project-aware command center: pick a project, configure models and agents, plan in chat, run work from the task board, review diffs, and automate the follow-up work that should happen again.

The canonical, published documentation lives at <a href="https://docs.openvibely.ai" target="_blank" rel="noopener noreferrer">docs.openvibely.ai</a>. This directory keeps concise in-repo guides for contributors and local reference.

## Start Here

| Goal | Local Guide | Published Docs |
|---|---|---|
| Understand the product | This page | <a href="https://docs.openvibely.ai/features-overview" target="_blank" rel="noopener noreferrer">Features Overview</a> |
| Run it locally | [Project README](../README.md) | <a href="https://docs.openvibely.ai/installation" target="_blank" rel="noopener noreferrer">Installation</a> |
| Complete first setup | [Project Setup User Guide](./project-setup-user-guide.md) | <a href="https://docs.openvibely.ai/first-time-setup" target="_blank" rel="noopener noreferrer">First-Time Setup</a> |
| Configure environment variables | [Environment Variables](./environment.md) | <a href="https://docs.openvibely.ai/configuration" target="_blank" rel="noopener noreferrer">Configuration</a> |
| Use the app day to day | Guides below | <a href="https://docs.openvibely.ai/quickstart" target="_blank" rel="noopener noreferrer">Quickstart</a> |

## App Mental Model

OpenVibely is organized around a selected project. The sidebar project selector changes the context for the main product surfaces.

| App Area | What Users Do There |
|---|---|
| Project selector | Choose the repository/workspace that tasks, chat, memory, workers, schedules, and integrations apply to. |
| Chat | Ask questions, plan changes, attach context, and orchestrate work conversationally. |
| Tasks | Create AI coding tasks, run them, inspect output, review changed files, and follow up. |
| Automations | Build supported graphs from schedules, Tasks and Agents, Native approvals, GitHub actions, and outcomes, then watch current state in the Live graph. |
| Schedule | Put individual project Tasks on a calendar so they run once or repeat. |
| Insights | Use grades, pulse, reflection, and analytics (including token usage, cost, and model breakdowns) to understand activity, history, and trends. |
| System | Configure alerts, models, agents, workers, channels, and personality. |

## Local Guides

| Area | Guide |
|---|---|
| Projects | [Project Setup User Guide](./project-setup-user-guide.md) |
| Models | [Models User Guide](./models-user-guide.md) |
| Agents | [Agents User Guide](./agents-user-guide.md) |
| Lifecycle hooks and skills | [Lifecycle Hooks and Skills User Guide](./lifecycle-skills-user-guide.md) |
| Workers | [Workers User Guide](./workers-user-guide.md) |
| Tasks | [Tasks User Guide](./tasks-user-guide.md) — includes Task Goals and Diff Review |
| Task Goals | See [Tasks User Guide § Task Goals](./tasks-user-guide.md#task-goals) and <a href="https://docs.openvibely.ai/task-goals" target="_blank" rel="noopener noreferrer">Task Goals (full reference)</a> |
| Diff review | See [Tasks User Guide § Diff Review](./tasks-user-guide.md#diff-review) and <a href="https://docs.openvibely.ai/task-diffs-review" target="_blank" rel="noopener noreferrer">Task Diffs & Review (full reference)</a> |
| Task chaining | <a href="https://docs.openvibely.ai/task-chaining" target="_blank" rel="noopener noreferrer">Task Chaining & Branch Lineage</a> |
| Chat | [Chat User Guide](./chat-user-guide.md) |
| Automation Graphs | See [Automation Graphs](#automation-graphs) below. |
| Schedule | [Schedule User Guide](./schedule-user-guide.md) |
| Insights | [Insights User Guide](./insights-user-guide.md) |
| Alerts | <a href="https://docs.openvibely.ai/alerts" target="_blank" rel="noopener noreferrer">Alerts (docs site)</a> |
| Attachments | <a href="https://docs.openvibely.ai/attachments" target="_blank" rel="noopener noreferrer">Attachments As Context (docs site)</a> |
| Git worktrees | <a href="https://docs.openvibely.ai/git-worktrees" target="_blank" rel="noopener noreferrer">Git Worktrees & Merge Safety (docs site)</a> |
| Environment variables | [Environment Variables](./environment.md) |

## Automation Graphs

Automation Graphs turns supported OpenVibely capabilities into one visual, project-scoped automation. Start from a maintained template, describe the behavior, or open a blank canvas. Add and configure Schedule, Task, Native approval, supported GitHub, and Outcome nodes, then connect deterministic handoffs from each node's right output to the next node's left input.

The web builder keeps new and edited graphs only in browser memory until `Save changes`; refreshing or navigating away discards unsaved edits. Save validates the complete graph and immediately creates or replaces the Automation's one current saved graph and required Tasks or Scheduler rows. There are no persisted editable drafts, graph versions, Definition page, or Automation history page.

Open a saved Automation to see its full-width Live graph. Current runtime state appears on the graph itself. Schedule and Task nodes link to their exact bound project Task; other nodes stay visual. Native approval and GitHub review, merge, release, and deployment boundaries remain human-controlled.

Use Schedule when one Task simply needs to run once or repeat. Use Automation Graphs when work needs multiple supported capabilities and visible handoffs. Existing Tasks, schedules, Alerts, or GitHub objects never become Automations automatically.

## Channels

| Channel | Guide |
|---|---|
| Slack | [Slack Channels Setup](./slack-channels-setup.md) |
| Discord | [Discord Channels Setup](./discord-channels-setup.md) |
| Telegram | [Telegram Channels Setup](./telegram-channels-setup.md) |
| Email | [Email Channel Setup](./email-channels-setup.md) |
| GitHub | [GitHub Channels Setup](./github-channels-setup.md) |
| GitHub autonomous SDLC | [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md) |
| OpenVibely-native autonomous SDLC | [OpenVibely-Native Autonomous SDLC User Guide](./openvibely-native-autonomous-sdlc-user-guide.md) |
