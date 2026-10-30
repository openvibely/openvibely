package database

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
)

func TestReplayItemMigrationRoundTripAndRollback(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "round-trip", true: "invalid-source-rollback"}[invalid], func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "replay.db"))
			goose.SetBaseFS(migrations.FS)
			if err := goose.SetDialect("sqlite3"); err != nil {
				t.Fatal(err)
			}
			if err := legacyTranscriptTo(db, 216); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p217','migration'); INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('t217','p217','migration','backlog',2,'completed','test'); INSERT INTO executions(id,task_id,status) VALUES('e217','t217','completed')`); err != nil {
				t.Fatal(err)
			}
			originals := []string{`{"responses_input":[{"type":"reasoning","encrypted_content":"opaque"},{"role":"user","content":"héllo"}],"future":true}`, `{"anthropic_execution_environment":"env","messages":[{"role":"user","content":[{"type":"tool_result","content":"done"}]}]}`, `[]`, ``}
			for i, raw := range originals {
				if _, err := db.Exec(`INSERT INTO execution_replay_messages(execution_id,sequence,user_content,assistant_content,reasoning_content,transcript_json) VALUES('e217',?,'user','answer','reason',?)`, i, raw); err != nil {
					t.Fatal(err)
				}
			}
			if invalid {
				if _, err := db.Exec(`UPDATE execution_replay_messages SET transcript_json='broken' WHERE sequence=2`); err != nil {
					t.Fatal(err)
				}
			}
			err := legacyTranscriptTo(db, 217)
			if invalid {
				if err == nil {
					t.Fatal("malformed transcript accepted")
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM execution_replay_messages`).Scan(&count); err != nil || count != len(originals) {
					t.Fatalf("source lost: %d, %v", count, err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='conversation_items'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial migration survived: %d, %v", count, err)
				}
				version, err := goose.GetDBVersion(db)
				if err != nil || version != 216 {
					t.Fatalf("bad failed migration version %d: %v", version, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='execution_replay_messages'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("old table not removed: %d, %v", count, err)
			}
			if err := legacyTranscriptTo(db, 216); err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(`SELECT sequence,user_content,assistant_content,reasoning_content,transcript_json FROM execution_replay_messages ORDER BY sequence`)
			if err != nil {
				t.Fatal(err)
			}
			restored := 0
			for rows.Next() {
				var i int
				var user, assistant, reason, raw string
				if err := rows.Scan(&i, &user, &assistant, &reason, &raw); err != nil {
					t.Fatal(err)
				}
				if user != "user" || assistant != "answer" || reason != "reason" {
					t.Fatal("lost frame metadata")
				}
				var before, after any
				if originals[i] != "" {
					if err := json.Unmarshal([]byte(originals[i]), &before); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(raw), &after); err != nil {
						t.Fatal(err)
					}
				} else if raw != "" {
					t.Fatal("text-only frame changed")
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("frame %d changed: %s", i, raw)
				}
				restored++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if restored != len(originals) {
				t.Fatal("missing frames")
			}
			if err := legacyTranscriptTo(db, 217); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDisplayMigrationBackfillsBytesAndRoundTrips(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "display.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := legacyTranscriptTo(db, 216); err != nil {
		t.Fatal(err)
	}
	const output = "hé\n[Using tool: Bash | ls]\n[Tool Bash done]\nx &amp; &#91;\n[/Tool]\n世"
	if _, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p216','migration'); INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('t216','p216','migration','backlog',2,'completed','test'); INSERT INTO executions(id,task_id,status,output) VALUES('e216','t216','completed','hé');
 INSERT INTO execution_output_chunks(execution_id,output) VALUES(?,?),(?,?)`, "e216", "\n[Using tool: Bash | ls]\n[Tool Bash done]\nx &amp; &#91;\n[/Tool]\n", "e216", "世"); err != nil {
		t.Fatal(err)
	}
	if err := legacyTranscriptTo(db, 217); err != nil {
		t.Fatal(err)
	}
	var total, rows int
	if err := db.QueryRow(`SELECT COALESCE(sum(display_bytes),0),count(*) FROM conversation_items WHERE execution_id='e216' AND lane='display' AND is_current=1`).Scan(&total, &rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 || total != len(output) {
		t.Fatalf("display bytes = %d over %d rows, want %d", total, rows, len(output))
	}
	if err := legacyTranscriptTo(db, 216); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(`SELECT output FROM executions WHERE id='e216'`).Scan(&restored); err != nil || restored != output {
		t.Fatalf("restored output = %q, %v", restored, err)
	}
	if err := legacyTranscriptTo(db, 217); err != nil {
		t.Fatal(err)
	}
}
