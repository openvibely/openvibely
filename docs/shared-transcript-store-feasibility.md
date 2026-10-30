# Shared transcript store: table-count feasibility

This checks the combined scope requested after the [persistence audit](transcript-persistence-map.md): provider persistence, compaction/recovery, display rendering, streaming and reconnect are to move together. Leaving output chunks and the old display implementation as a permanent parallel path is not the target.

## Conclusion

A separate `conversation_contexts` table is not structurally required by the paths inspected. One shared item/change table plus the existing execution and thread-checkpoint metadata can represent the required ownership, context boundaries, partial content and revisions.

This is a feasibility conclusion, not proof that a finalized production schema has the globally minimum table count or optimal performance. A single table can hide multiple record types; reducing table count alone is not an optimization. Existing queue and tool-delivery tables still have independent transactional responsibilities.

## Proposed table budget

| Table | Target |
|---|---|
| `conversation_items` | One new shared table for structured item snapshots, incremental changes and item revisions, including checkpoint-owned replacement items. |
| `execution_replay_messages` | Migrate contents/metadata, then remove. |
| `execution_output_chunks` | Migrate display text into structured display items, replace readers/writers, then remove in the same project. |
| `executions` | Retain execution lifecycle/metrics/identity. Add replay interpretation, cursor/boundary metadata as needed. Move aggregate display/reasoning payload consumers to the shared store; do not maintain full output as a second transcript authority. |
| `chat_compaction_checkpoints` | Retain thread scope, active replacement generation, covered execution/item boundary, compatibility and independent session metadata. Migrate nested/native transcript payloads into shared items. |
| `thread_inputs` | Retain queued/accepted input ownership and acceptance transactions. |
| `openai_async_tool_calls` | Retain durable tool execution and delivery state; preserve links to transcript call IDs. |

No additional contexts table. Net transcript-table change: add one, remove two. This does not imply that the whole application has only these tables, or that all metadata columns are finalized.

## Why a simple message sequence is insufficient

A message can change after a client has already received it. Current Anthropic code accumulates parallel tool results in an existing message; approval recovery can amend an earlier exchange. Updating message 5 in place while a client is waiting for messages after 10 would hide that update.

Use two separate concepts:

- Stable item identity/position: which message or tool record is being changed.
- Monotonically increasing change sequence: what changed since a client's last cursor.

Both fit in the same table. Partial text changes and replacements are typed records, not fragments of a giant serialized conversation. A bounded full snapshot of an individual item allows restoration to start from its latest snapshot and apply subsequent deltas. Indexes support owner/sequence and owner/item/snapshot lookups.

This trades some additional event rows and item reconstruction for avoiding full-conversation rewrites. Completed-item snapshots can duplicate their earlier deltas until the snapshot write deletes the item's superseded revisions; reconnect reads current heads only, so no older revision is referenced after pruning. It is not a zero-cost design or a deduplication solution by itself.

## Executable feasibility check

Run:

```sh
python3 scripts/replay-memory-bench/check_shared_store.py
```

The script uses synthetic records in an in-memory SQLite database, minimal stand-ins for existing metadata tables, and only one new payload table. It validates:

- Partial Unicode text and completed-item reconstruction.
- A later revision to an earlier tool item, visible through a change cursor.
- Unique owner/change sequences.
- Rollback of steering acceptance when transcript persistence fails.
- Atomic checkpoint generation/source-cursor changes.
- Preservation of original display items after context compaction.
- Continuation after the checkpoint boundary.
- Failed next-generation compaction leaving the prior checkpoint active.

The catch-up plan was:

```text
SEARCH conversation_items USING INDEX sqlite_autoindex_conversation_items_1
(owner_type=? AND owner_id=? AND seq>?)
```

Thus catch-up can seek directly to new events without the current cumulative chunk-length scan. No context-table join is needed for this query. Recovery still reads existing checkpoint metadata; this is already required today.

## Requirements still to implement and validate

This synthetic proof is deliberately not production DDL. Before migration, implementation must specify and test:

1. Foreign-key/deletion ownership for execution and checkpoint-owned items; sequence allocation, retries and idempotency under concurrent writes.
2. Explicit provider format, append-versus-replacement semantics, item order, retained-user identity, source boundaries and generation metadata. Do not infer these by scanning JSON.
3. Display projections and visibility: provider-only encrypted/reasoning/system state must not be sent to the UI simply because it shares storage. Display-only progress and failed-attempt output must not become model context. One store does not imply identical provider and display payloads.
4. Atomic provider checkpoint, steering and async-tool delivery integration. Native compaction/session settings retain their separate semantics.
5. A migration for every audited format, including text-only old executions. Missing original provider details cannot be invented; convert available display content once into an explicitly typed representation read by the new path.
6. Structured provider/client emission, browser rendering, cold-page item pagination, reconnect protocol and cache invalidation. Do not merely move the old marker string to another table and keep splitting it on each load.
7. Full-context reconstruction cost, large partial items, superseded-delta retention, active references and real SQLite/Go memory behavior. Implemented: `BenchmarkTranscriptPath` compares the complete implementation against the previous path, covering streaming, reconnect, replay checkpoints, compaction and recovery. Historical results and tradeoffs are archived via [benchmark evidence](../scripts/replay-memory-bench/EVIDENCE.md); the current harnesses and latest summary are in [benchmarks](../scripts/replay-memory-bench/README.md). Reconnect is constant-time, writes are slower per chunk, and superseded revisions are pruned on put, remove or reset.

The proof establishes that another contexts table is unnecessary for the tested invariants. It does not claim that removing a table by itself lowers database or application processing.
