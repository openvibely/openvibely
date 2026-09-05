# Workers User Guide

Use `/workers` to control execution concurrency and monitor queue pressure.

## What This Page Is For

The Workers page controls how much work OpenVibely is allowed to run at the same time.

Why this matters:

- If limits are too low, tasks wait in queue longer.
- If limits are too high, your machine or model providers can get overloaded.
- This page helps you balance speed (throughput) vs stability.

## The Workers Page

When you open Workers you see three things:

**Live stats header** — Updates every few seconds. Shows:
- **Worker Pool Size** — the current global limit (how many execution slots exist)
- **Tasks Running** — how many slots are active right now, shown as `running / pool size`
- **Queue** — how many tasks are waiting for a free slot

**Worker Capacity & Utilization table** — Global and every project in one view. The table auto-refreshes every 3 seconds. Columns:

- `Scope`: whether the row is `Global` (all projects) or a single project.
- `Name`: the project name (or `All Projects` for global).
- `Running`: tasks currently executing.
- `Queue`: tasks waiting for an available worker slot.
- `Limit`: max concurrent workers allowed for that scope — editable inline, click, type, click **Set**.
- `Status`: quick health signal (`Idle`, `Active`, `At capacity`).

**Per-Model Worker Pools table** — Only appears if at least one model has a dedicated worker pool configured. Limits for model pools are set on the `/models` page, not here.

## Change Global Limit

In the `Global` row, edit the limit value and click `Set`. This sets the hard ceiling across every project.

Enter any non-negative whole number. `0` or an empty value means `Unlimited`; positive values are finite ceilings and are not restricted by a product-level maximum.

Use this when the whole app feels slow due to queueing, or when your machine needs stricter load control. Lowering the global limit does not cancel tasks that are already running. Those reservations finish normally, while new admissions wait until actual global usage is below the new ceiling.

## Change Per-Project Limit

Find a project row, edit the limit value, and click `Set`. A project may use any positive whole-number limit up to the current finite global limit. Setting a project to `0` or leaving it empty removes the project-specific cap, so that project inherits the global limit.

Project caps are independent maximums, not a combined allocation. For example, two projects may each be configured for 25 workers under a global limit of 25; they can share the pool, but actual concurrent usage across both projects never exceeds 25.

If the global limit is lowered below an existing project cap, the Workers page marks that project `Exceeds global` and asks you to lower the project cap before saving other project-limit changes. Running work is not cancelled, and new work remains governed by the actual global ceiling.

The page preserves any limit field you're actively editing during live refreshes, so typing a new value won't get overwritten before you click `Set`.

## How The Two Layers Work Together

Global and project limits stack as a dual-layer cap:

- A task needs to fit within **both** the global limit and its project's limit (if one is set) before it can run.
- If a project has no limit set (`0`), it competes freely within the global pool.
- If a project limit is set lower than the global limit, that project can never consume more than its own cap regardless of how many global slots are free.

**Example:** Global = 5, Project A = 2, Project B = no limit. Project A can run at most 2 tasks at once even if 4 global slots are free. Project B can use up to all 5 global slots if nothing else is running.

## Per-Model Worker Pools

If a model has a dedicated worker pool (`Max Workers > 0` in `/models`), it appears in the `Per-Model Worker Pools` table.

Why use per-model pools:

- Prevent expensive/slower models from consuming all worker slots.
- Give critical models predictable throughput.

Model pool limits are configured from `/models`.

## Reading Status Quickly

| Badge | Meaning |
|---|---|
| `Idle` | No tasks running in this scope. |
| `Active` | Tasks are running and slots remain available. |
| `At capacity` | All allowed slots for this scope are taken; new work queues. |

A non-zero Queue with `At capacity` status means tasks are waiting. They will be dispatched as soon as a slot frees.

## Quick Tuning Patterns

- Many queued tasks across all projects: raise `Global` limit first.
- One project always queued while others are idle: raise that project's limit.
- Provider/model-specific bottleneck: configure per-model pool limits in `/models`.
