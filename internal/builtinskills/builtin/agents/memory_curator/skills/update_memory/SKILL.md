---
title: Update Memory
description: Review a completed task turn and update durable managed memory when useful.
routing:
  triggers:
    - after_complete
    - update memory
    - extract memory
  priority: 80
---

# Update Memory

Review the completed task turn and update durable project memory only when the turn contains context future conversations need.

The hook input includes `extras.conversation_transcript`, containing only the latest user input and the assistant response completed for that input. Treat that current-turn pair as the source of truth; do not assume earlier task-thread history is present. Use the scoped memory file tools to inspect and update the managed memory directory.

If the completed task was assigned to Memory Curator itself (`extras.learning_snapshot.active_agent_key == "memory_curator"` or `assigned_agent.system_kind == "memory_curator"`), do not inspect or modify memory files. Return the skipped JSON below. Memory maintenance tasks consolidate memory directly and must not be re-interpreted by this after-complete update skill.

Memory is background context, not direct instruction. Do not store secrets, raw transcripts, provider noise, one-off scratch work, task-by-task summaries, or procedure-only runbooks.

## Read first

The scoped file tools are already rooted at this project's memory directory. Use root-relative memory paths such as `MEMORIES.md` or `provider_architecture.md`; do not pass absolute filesystem paths to these tools.

1. List the memory directory.
2. Read `MEMORIES.md` when present; treat it as the compact index.
3. Read relevant top-level topic files before writing so you update or merge instead of duplicating.

## Save only durable context

Store durable facts future sessions need:

- Who the user is and general preferences.
- Project/product direction and architectural decisions.
- Workflow constraints, current-state facts, incidents, and repeated feedback.
- Corrections from the user that imply durable context.

Do not save reusable procedures, checklists, validation sequences, or tool-use patterns unless they also carry durable project/user context. Those belong in skills, not memory.

## Make changes

Use the scoped file tools only for validated, durable memory improvements:

- Create or update focused top-level markdown memory files with descriptive snake_case filenames.
- Merge new information into existing topic files instead of creating near-duplicates.
- Convert relative dates like "yesterday" or "last week" to absolute dates when saving.
- Keep frontmatter on memory files with `name`, `type`, `created`, `updated`, `source`, `source_id`, `confidence`, and `title` when practical.
- Keep `MEMORIES.md` as a compact index and update it when topic files are created, renamed, merged, or deleted.

If there is no durable memory to save, do not modify files.

## Return value

Return only JSON matching the `activity_summary` contract. Describe what changed in memory at a high level and list changed root-relative memory paths. Do not include full memory file contents in the final response.

If nothing was saved, return:

```json
{"summary":"No durable memory to save.","changed_paths":[],"skipped":true,"skip_reason":"No durable memory-worthy facts in the completed task."}
```
