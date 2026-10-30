"""SQLite feasibility check, not a production migration or performance benchmark.

Run: python3 scripts/replay-memory-bench/check_shared_store.py
Uses an in-memory database and synthetic data only. Existing metadata tables are
represented minimally. One new table holds item snapshots and incremental events.
"""
import json
import sqlite3


def check():
    db = sqlite3.connect(":memory:", isolation_level=None)
    db.executescript("""
        CREATE TABLE executions(id TEXT PRIMARY KEY);
        CREATE TABLE thread_inputs(id TEXT PRIMARY KEY, applied INTEGER NOT NULL);
        CREATE TABLE chat_compaction_checkpoints(
            scope_type TEXT, scope_id TEXT, active_generation INTEGER,
            source_execution_id TEXT, source_sequence INTEGER,
            PRIMARY KEY(scope_type, scope_id)
        );
        INSERT INTO executions VALUES('exec1');
        INSERT INTO thread_inputs VALUES('steer1',0);
        CREATE TABLE conversation_items(
            owner_type TEXT NOT NULL,
            owner_id TEXT NOT NULL,
            seq INTEGER NOT NULL,
            generation INTEGER NOT NULL,
            item_id TEXT NOT NULL,
            operation TEXT NOT NULL CHECK(operation IN ('replace','append_text')),
            payload TEXT NOT NULL CHECK(json_valid(payload)),
            PRIMARY KEY(owner_type,owner_id,seq)
        );
        CREATE INDEX item_versions ON conversation_items(
            owner_type,owner_id,generation,item_id,seq
        );
        CREATE INDEX item_snapshots ON conversation_items(
            owner_type,owner_id,generation,item_id,seq
        ) WHERE operation='replace';
    """)

    def write(seq, item, payload, op="replace", owner="execution", key="exec1", gen=0):
        db.execute("INSERT INTO conversation_items VALUES(?,?,?,?,?,?,?)",
                   (owner, key, seq, gen, item, op, json.dumps(payload)))

    def restore(item, owner="execution", key="exec1", gen=0):
        row = db.execute("""SELECT seq,payload FROM conversation_items
            WHERE owner_type=? AND owner_id=? AND generation=? AND item_id=?
            AND operation='replace' ORDER BY seq DESC LIMIT 1""",
                         (owner, key, gen, item)).fetchone()
        if row is None:
            raise ValueError("missing item snapshot")
        seq, raw = row
        value = json.loads(raw)
        for op, raw in db.execute("""SELECT operation,payload FROM conversation_items
                WHERE owner_type=? AND owner_id=? AND generation=? AND item_id=?
                AND seq>? ORDER BY seq""", (owner, key, gen, item, seq)):
            assert op == "append_text"
            value["content"] += json.loads(raw)["text"]
        return value

    catchup_sql = """SELECT seq,item_id,operation,payload FROM conversation_items
        WHERE owner_type=? AND owner_id=? AND seq>? ORDER BY seq LIMIT 100"""

    # Streaming partial text uses structured deltas, including Unicode.
    write(1, "answer", {"role": "assistant", "content": ""})
    write(2, "answer", {"text": "hello "}, "append_text")
    write(3, "answer", {"text": "世界"}, "append_text")
    assert restore("answer")["content"] == "hello 世界"

    # A completion snapshot bounds future reconstruction to this item only.
    write(4, "answer", restore("answer"))
    assert restore("answer")["content"] == "hello 世界"

    # An earlier tool message can be revised after later events exist.
    write(5, "tool", {"tool_results": [{"id": "c1", "text": "first"}]})
    write(6, "other", {"role": "assistant", "content": "working"})
    revised = {"tool_results": [{"id": "c1", "text": "first"},
                                {"id": "c2", "text": "second"}]}
    write(7, "tool", revised)
    assert restore("tool") == revised
    missing = db.execute(catchup_sql, ("execution", "exec1", 6)).fetchall()
    assert [(row[0], row[1]) for row in missing] == [(7, "tool")]

    # Sequence uniqueness prevents accidental replay insertion at the same cursor.
    try:
        write(7, "tool", revised)
    except sqlite3.IntegrityError:
        pass
    else:
        raise AssertionError("duplicate sequence accepted")

    # Steering acceptance cannot survive failure to save its conversation item.
    db.execute("BEGIN IMMEDIATE")
    try:
        db.execute("UPDATE thread_inputs SET applied=1 WHERE id='steer1'")
        write(7, "steer", {"role": "user", "content": "change direction"})
    except sqlite3.IntegrityError:
        db.execute("ROLLBACK")
    assert db.execute("SELECT applied FROM thread_inputs").fetchone() == (0,)
    db.execute("BEGIN IMMEDIATE")
    db.execute("UPDATE thread_inputs SET applied=1 WHERE id='steer1'")
    write(8, "steer", {"role": "user", "content": "change direction"})
    db.execute("COMMIT")

    # A checkpoint generation and its source cursor commit together, retaining
    # the display events in the execution owner. No context table is required.
    native = {"type": "compaction", "encrypted_content": "opaque-provider-state"}
    db.execute("BEGIN IMMEDIATE")
    write(1, "native", native, owner="task_checkpoint", key="task1", gen=1)
    db.execute("INSERT INTO chat_compaction_checkpoints VALUES('task','task1',1,'exec1',8)")
    db.execute("COMMIT")
    assert restore("native", "task_checkpoint", "task1", 1) == native
    assert restore("answer")["content"] == "hello 世界"
    write(9, "continuation", {"role": "assistant", "content": "after compaction"})
    boundary = db.execute("SELECT source_sequence FROM chat_compaction_checkpoints").fetchone()[0]
    assert [r[0] for r in db.execute(catchup_sql, ("execution", "exec1", boundary))] == [9]

    # Simulated failed next compaction leaves the old generation active.
    db.execute("BEGIN IMMEDIATE")
    write(2, "native", {"type": "compaction", "encrypted_content": "next"},
          owner="task_checkpoint", key="task1", gen=2)
    db.execute("UPDATE chat_compaction_checkpoints SET active_generation=2")
    db.execute("ROLLBACK")
    assert db.execute("SELECT active_generation FROM chat_compaction_checkpoints").fetchone() == (1,)
    assert db.execute("SELECT count(*) FROM conversation_items WHERE generation=2").fetchone() == (0,)

    plan = " ".join(r[3] for r in db.execute("EXPLAIN QUERY PLAN " + catchup_sql,
                                           ("execution", "exec1", 6)))
    assert "SEARCH" in plan and "seq>?" in plan and "TEMP B-TREE" not in plan, plan
    print("PASS: partial text, item revisions, reconnect, unique cursors, steering rollback,")
    print("      compaction generations, retained display, continuation and compaction rollback")
    print("Catch-up query plan:", plan)
    db.close()


if __name__ == "__main__":
    check()
