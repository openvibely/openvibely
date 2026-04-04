# Workers User Guide

Use `/workers` to control execution concurrency and monitor queue pressure.

## What This Page Is For

The Workers page controls how much work OpenVibely is allowed to run at the same time.

Why this matters:

- If limits are too low, tasks wait in queue longer.
- If limits are too high, your machine or model providers can get overloaded.
- This page helps you balance speed (throughput) vs stability.

## Worker Capacity & Utilization Table

The main table combines global and per-project limits so you can see capacity and pressure in one place.

### What each column means

- `Scope`: whether the row is `Global` (all projects) or a single project.
- `Name`: the project name (or `All Projects` for global).
- `Running`: tasks currently executing.
- `Queue`: tasks waiting for an available worker slot.
- `Limit`: max concurrent workers allowed for that scope.
- `Status`: quick health signal (`Idle`, `Active`, `At capacity`).

How to use this:

- High `Queue` + frequent `At capacity` means increase limits (if your machine/provider can handle it).
- Low `Running` with little/no queue usually means you can keep limits as-is.

## Change Global Limit

1. In the `Global` row, edit the limit value.
2. Click `Set`.

This sets the top-level cap across every project.

Use this when the whole app feels slow due to queueing, or when your machine needs stricter load control.

## Change Per-Project Limit

1. Find a project row.
2. Edit the limit value.
3. Click `Set`.

Project limits reserve/fence capacity per project within the global cap.

Use this when one busy project is starving other projects, or when a specific project should run faster than others.

## Per-Model Worker Pools

If a model has a dedicated worker pool (`Max Workers > 0` in `/models`), it appears in the `Per-Model Worker Pools` table.

Why use per-model pools:

- Prevent expensive/slower models from consuming all worker slots.
- Give critical models predictable throughput.

Model pool limits are configured from `/models`.

## Reading Status Quickly

- `At capacity`: running workers reached limit.
- `Active`: work is running below limit.
- `Idle`: no running work.
- Queue > 0 indicates backlog pressure.

## Quick Tuning Patterns

- Many queued tasks across all projects: raise `Global` limit first.
- One project always queued while others are idle: raise that project's limit.
- Provider/model-specific bottleneck: configure per-model pool limits in `/models`.
