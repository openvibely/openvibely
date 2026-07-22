---
title: Observe Task For Learning
description: Learn from one completed task and update durable skill configuration when useful.
routing:
  triggers:
    - after_complete
    - learning
  priority: 80
---

# Observe Task For Learning

Review the completed task turn and decide whether durable skill configuration should change.

The hook input includes `extras.conversation_transcript`, containing only the latest user input and the assistant response completed for that input. Treat that current-turn pair as the source of truth; do not assume earlier task-thread history is present. Use the learning snapshot and current response for selected skills, errors, validation commands, user corrections, and reusable workflow evidence available in this turn.

Be active in preserving reusable learning. Strong signals include user corrections, repo-specific workflows, non-trivial debugging paths, repeated validation sequences, missing or stale skill guidance, missing skill coverage, or anything the next similar task should know before starting.

Prefer the smallest durable skill change that will help future tasks:

1. Patch a skill used or clearly implicated by this task.
2. Patch an existing umbrella skill.
3. Add a support file under an existing skill.
4. Create a new reusable skill when no existing skill fits.
5. Consolidate duplicate standalone skills when the overlap is clear.

Before deciding to save learning, inspect the hook input's `learning_snapshot`. It names the assigned agent, selected agent-owned skills, selected standalone skills, and write policy. Inspect the existing standalone library with read tools (`skills_list`, `skill_view`) and use `agent_view` only to understand assigned-agent purpose when relevant. Do not change agent prompts, routing, tools, permissions, lifecycle hooks, selectable state, or skill attachments.

Missing skill coverage is not a no-op reason. If the transcript contains durable reusable learning and no suitable standalone skill exists, create the smallest appropriate standalone skill. If the task had an assigned agent and the learning is specific to that agent's role, workflow, or selected agent-owned skill, use `agent_skill_manage` instead. Do not create a new agent or attach standalone skills to an agent as a fallback.

Use mutation tools only for durable reusable skill changes. Use `skill_import` when importing a complete existing standalone skill package; use `skill_manage` to create, patch, archive, write support files, or remove stale support files for standalone skills. Use `agent_skill_manage` only for skills owned by the task's assigned agent; pass only the skill key, never an agent/skill path. Agent creation, editing, skill attachment, routing changes, and archival are disabled for autonomous maintenance by product policy.

## Skill Package Layout

A standalone skill is a package:

```text
skills/<skill_key>/
  SKILL.md
  references/
  templates/
  scripts/
  assets/
```

Keep `SKILL.md` concise and procedural: when to use the skill, key steps/checks, common pitfalls, and stable validation guidance. Put bulky or specialized material in support files:

- `references/` for longer explanations, API notes, troubleshooting matrices, or background details that are still procedural.
- `templates/` for reusable prompt, config, patch, checklist, or document templates.
- `scripts/` for executable helper scripts that future task turns can call with normal runtime tools using the `skill_dir`/`scripts_dir` paths returned by `skill_view`.
- `assets/` for fixtures, sample payloads, schemas, examples, or static data.

When inspecting a skill, `skill_view({"handle":"<skill>"})` returns `SKILL.md`, linked support-file metadata, and absolute `skill_dir`/`scripts_dir` paths. Load support files selectively with `skill_view({"handle":"<skill>","file_path":"references/example.md"})`; do not load every support file by default. When adding a script, make `SKILL.md` explain when to call it and include concrete command examples using `${OPENVIBELY_SKILL_DIR}` or the returned `scripts_dir` path.

Do not inline large references, scripts, templates, or fixtures into `SKILL.md` when a support file would keep the main skill clearer.

## What Belongs In A Skill

Create or patch a skill only when the completed task reveals reusable execution guidance for a recurring class of work.

Good skill material changes how a future task should be performed: workflows, validation sequences, debugging paths, tool-use procedures, implementation patterns, concrete pitfalls, and actionable corrections.

Do not turn background context into a skill unless it directly changes future execution. User preferences, project direction, architecture decisions, workflow constraints, current-state facts, and repeated feedback should appear in a skill only as concise procedural rules or checks.

If a lesson is only contextual, do not create or patch a skill. If it is procedural, prefer patching an existing umbrella skill over creating a narrow duplicate.

When this completed task clearly proves two standalone skills overlap, consolidate immediately instead of creating another sibling: patch the surviving skill with any useful reusable content, then call `skill_manage(action=archive, absorbed_into="<survivor>")` for the redundant skill. Only defer to scheduled maintenance when the duplicate relationship is uncertain.

After a successful skill mutation, the mutation tool maintains the minimal top-level `skills/SKILLS.md` skill-link index for create/patch/archive actions. Do not attempt direct index-file edits.

Do not create durable artifacts for one-off facts, temporary task state, guesses not supported by the transcript, trivial command usage, or information already covered by an existing skill.

Return only one raw JSON object matching the `learning_summary` contract. Do not include markdown, prose, headings, or commentary outside the JSON.

If there is no durable learning to save, return exactly:

```json
{"summary":"No durable learning to save.","nothing_to_save":true}
```
