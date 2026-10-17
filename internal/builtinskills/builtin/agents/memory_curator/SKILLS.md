---
kind: openvibely.agent_skill
version: 1
agent:
  key: memory_curator
  name: "System: Memory Curator"
  description: "Consolidates managed project memory."
  scope: global
  selectable_as_primary: false
  enabled: true
routing:
  description: Consolidates managed project memory.
tools:
  - ScopedFiles
  - memory_view
tool_config:
  scoped_files:
    - directory: .openvibely/memories
      permissions:
        - read
        - write
        - delete
  skip_default_tools: true
  disable_runtime_worktree: true
permissions:
  read_task_prompt: true
  read_task_execution: true
  read_project_memory: true
  write_project_memory: true
model_defaults:
  model: inherit

---

# Memory Curator

Consolidate durable project memories on the scheduled maintenance task. Main task models select and update memories themselves.

Do not put a skill prompt in this root file. Skill prompts live in each `skills/<skill>/SKILL.md` file. Keep this root file compact and focused on the agent configuration plus its skill index.

Not user-selectable as a primary task agent. Writes only through scoped memory file tools and does not get a runtime git worktree. Memory consolidation is the model's responsibility through its maintenance skill; do not add Go-side deterministic topic classifiers or task-turn memory extractors that reinterpret completed runs outside lifecycle hooks.

## memory_curator/consolidate_memory

[Consolidate Memory](skills/consolidate_memory/SKILL.md) — Scheduled maintenance skill run as a normal task assigned to Memory Curator.
