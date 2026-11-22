package service

import (
	"context"
	"encoding/json"
	"fmt"
	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"strings"
	"testing"
)

func TestGeminiSkillResumePreservesHistory(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "preserve history", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	source := models.Execution{TaskID: task.ID, Status: models.ExecFailed}
	current := models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	for _, ex := range []*models.Execution{&source, &current} {
		if err := env.Executions.Create(ctx, ex); err != nil {
			t.Fatal(err)
		}
	}
	tail := models.ExecutionReplayMessage{TranscriptJSON: `[{"role":"user","content":"later saved message"}]`}
	replay := []models.ExecutionReplayMessage{{TranscriptJSON: `{"contents":[{"role":"model","parts":[{"functionCall":{"id":"skill","name":"skill_view","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"skill","name":"skill_view","response":{"error":"skill_view awaiting user input: question"}}}]}]}`}, tail}
	if err := env.Executions.ReplaceReasoningReplay(ctx, source.ID, "", replay); err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(map[string]any{"source_execution_id": source.ID, "question_id": "question", "arguments": json.RawMessage(`{}`), "output": "skill result"})
	later := models.Execution{ID: "later", PromptSent: "later user message", Output: "later assistant answer"}
	svc := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
	result, err := svc.completeInstallationTranscript(contracts.AgentRequest{Ctx: ctx, ExecID: current.ID, ProjectID: "default", Message: contracts.SkillInputResultPrefix + string(envelope), ChatHistory: []models.Execution{source, later}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChatHistory) != 2 || result.ChatHistory[1].ID != later.ID || result.ChatHistory[1].Output != later.Output {
		t.Fatalf("lost later history: %+v", result.ChatHistory)
	}
	saved, err := env.Executions.ReplayMessagesByExecutionIDs(ctx, []string{source.ID})
	if err != nil || len(saved[source.ID]) != 2 || saved[source.ID][1].TranscriptJSON != tail.TranscriptJSON {
		t.Fatalf("lost saved replay: %+v %v", saved, err)
	}
}

func TestGeminiSkillReplayCompletion(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("error=%v/missing=%v", failed, missing), func(t *testing.T) {
				call := json.RawMessage(`{"role":"model","parts":[{"thoughtSignature":"keep-signature","functionCall":{"id":"skill","name":"skill_view","args":{"handle":"fixture"}}},{"functionCall":{"id":"image","name":"view_image","args":{}}}]}`)
				response := `{"role":"user","parts":[{"functionResponse":{"id":"image","name":"view_image","response":{"output":"image result"},"parts":[{"inlineData":{"mimeType":"image/png","data":"YWJj"}}]}}`
				if !missing {
					response += `,{"functionResponse":{"id":"skill","name":"skill_view","response":{"error":"` + contracts.PendingSkillInputPrefix + `question"}}}`
				}
				response += `]}`
				replay := models.ReplayFromRows("contents", []json.RawMessage{call, json.RawMessage(response)})
				before := replay.FullTranscriptJSON()
				got, found, err := completeSkillToolReplay(replay, "question", json.RawMessage(`{"handle":"fixture"}`), "resolved result", failed, "skill")
				if err != nil || !found {
					t.Fatalf("found=%v err=%v", found, err)
				}
				if replay.FullTranscriptJSON() != before {
					t.Fatal("modified original replay")
				}
				if string(got.Rows.Items[0]) != string(call) {
					t.Fatal("changed call/signature")
				}
				var row struct {
					Parts []struct {
						FunctionResponse struct {
							ID       string
							Response map[string]any
							Parts    []json.RawMessage
						}
					}
				}
				if err := json.Unmarshal(got.Rows.Items[1], &row); err != nil {
					t.Fatal(err)
				}
				if len(row.Parts) != 2 {
					t.Fatalf("parts=%d", len(row.Parts))
				}
				for _, part := range row.Parts {
					f := part.FunctionResponse
					if f.ID == "skill" {
						key := "output"
						if failed {
							key = "error"
						}
						if f.Response[key] != "resolved result" || len(f.Response) != 1 {
							t.Fatalf("wrong result: %+v", f.Response)
						}
					}
					if f.ID == "image" && (f.Response["output"] != "image result" || len(f.Parts) != 1) {
						t.Fatalf("lost sibling image: %+v", f)
					}
				}
			})
		}
	}
}

func TestGeminiSkillMissingSiblingAndPendingApproval(t *testing.T) {
	for _, pending := range []bool{false, true} {
		rows := []json.RawMessage{json.RawMessage(`{"role":"model","parts":[{"functionCall":{"id":"skill","name":"skill_view","args":{}}},{"functionCall":{"id":"sibling","name":"skill_view","args":{"handle":"other"}}}]}`)}
		if pending {
			rows = append(rows, json.RawMessage(`{"role":"user","parts":[{"functionResponse":{"id":"sibling","name":"skill_view","response":{"error":"`+contracts.PendingSkillInputPrefix+`another"}}}]}`))
		}
		rows = append(rows, json.RawMessage(`{"role":"model","parts":[{"text":"later answer"}]}`))
		got, found, err := completeSkillToolReplay(models.ReplayFromRows("contents", rows), "question", json.RawMessage(`{}`), "done", false, "skill")
		if !found {
			t.Fatal("missing skill call")
		}
		if pending {
			if err == nil || !strings.Contains(err.Error(), "still awaiting") {
				t.Fatalf("resumed despite pending approval: %v", err)
			}
		} else if err != nil || !strings.Contains(got.FullTranscriptJSON(), "Tool execution was interrupted") {
			t.Fatalf("missing interrupted sibling: %v %s", err, got.FullTranscriptJSON())
		}
		if !strings.Contains(string(got.Rows.Items[len(got.Rows.Items)-1]), "later answer") {
			t.Fatal("result inserted after later answer")
		}
	}
}
