# Agents User Guide

Use `/agents` to define reusable agent behavior (prompting, tools, and plugins).

## What This Page Is For

An Agent is a reusable execution profile you can attach to tasks.

It defines things like:

- the system prompt/persona,
- allowed tools,
- plugin/runtime capabilities,
- optional model override.

Why this matters:

- Keeps behavior consistent across many tasks.
- Lets you separate “how to work” (agent) from “what to do” (task prompt).

## Create an Agent

1. Open `/agents`.
2. Click `+ Add Agent`.
3. In `Agent Details`, fill:
   - `Name`
   - `Description`
   - `System Prompt`
   - Optional model override (`Model`)
4. Choose allowed tools.
5. Click `Save`.

## Generate From Description

In `Agent Details`, enter a description and click `Generate`.

This drafts agent configuration from your description and currently selected plugin context.

Why use generate:

- Faster starting point for complex prompts.
- Good when you know outcome/role but do not want to handwrite the full system prompt.

## Plugin Management (Inside Agent Modal)

### Plugins Tab

- Search installed/available plugins.
- Toggle plugin selection for the current agent.
- Selected plugins become part of that agent's runtime.

Use this to control which capabilities are available during execution.

### Marketplace Tab

- Add marketplace source (repo/URL/path).
- Sync or remove marketplace entries.
- Restore default marketplaces.

Use this when the plugin you need is not already installed/visible.

## Install and Uninstall Plugins

Use controls in the modal plugin catalog to install/uninstall plugin packages.

Install is global to the app, while enablement is per-agent.

## Edit and Delete

From any agent card menu:

- `Edit`
- `Delete`

Deleting an agent unlinks tasks that referenced it.

## Using Agents in Tasks

On task create/edit forms, choose an `Agent` from the dropdown.

Agent-defined plugins and runtime behavior apply when that task runs.

Tip: use one agent per recurring workflow type (for example, implementation, code review, release prep) instead of one giant universal agent.
