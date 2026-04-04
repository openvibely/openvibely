package stream

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestParseJSONStream_ExtractsTextContent(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task for JSON Stream",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant"}}
{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}
{"type":"content_block_stop","index":0}
{"type":"message_delta","delta":{"stop_reason":"end_turn"}}
{"type":"message_stop"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "Hello world!"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}

	sw.Flush()
	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get updated execution: %v", err)
	}
	if updatedExec.Output != expected {
		t.Errorf("expected DB output %q, got %q", expected, updatedExec.Output)
	}
}

func TestParseJSONStream_HandlesToolUse(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task for Tool Use",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me help you with that."}}
{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"Read","input":{}}}
{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file"}}
{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"_path\":\"/path/to/file.go\"}"}}
{"type":"content_block_stop","index":1}
{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"I've read the file."}}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	if !strings.Contains(output, "Let me help you with that.") {
		t.Errorf("expected output to contain initial text, got %q", output)
	}
	if !strings.Contains(output, "[Using tool: Read | file.go]") {
		t.Errorf("expected output to contain tool use with file detail, got %q", output)
	}
	if !strings.Contains(output, "I've read the file.") {
		t.Errorf("expected output to contain follow-up text, got %q", output)
	}
}

func TestParseJSONStream_StreamEventWrapper(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Stream Event Wrapper",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me read the file."}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"Read","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"/src/main.go\"}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":1}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The file has 42 lines."}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"result","subtype":"success","is_error":false,"result":"The file has 42 lines."}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if !strings.Contains(output, "[Thinking]") {
		t.Errorf("expected thinking block, got %q", output)
	}
	if !strings.Contains(output, "Let me read the file.") {
		t.Errorf("expected thinking content, got %q", output)
	}
	if !strings.Contains(output, "[/Thinking]") {
		t.Errorf("expected thinking end marker, got %q", output)
	}

	if !strings.Contains(output, "[Using tool: Read | main.go]") {
		t.Errorf("expected tool marker with file detail, got %q", output)
	}

	if !strings.Contains(output, "The file has 42 lines.") {
		t.Errorf("expected text response, got %q", output)
	}

	if strings.Contains(output, "import js") || strings.Contains(output, "mport js") {
		t.Errorf("output contains garbled fragments: %q", output)
	}
}

func TestParseJSONStream_CompleteMessages(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Complete Messages",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"Let me check that for you."},{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"pwd"}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":50}}
{"role":"user","content":[{"type":"tool_result","content":"/home/user/project","tool_use_id":"toolu_01"}]}
{"id":"msg_02","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"You are in the /home/user/project directory."}],"stop_reason":"end_turn","usage":{"input_tokens":200,"output_tokens":30}}
{"role":"system","cost_usd":0.0123,"duration_ms":5000,"duration_api_ms":4800}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	if !strings.Contains(output, "Let me check that for you.") {
		t.Errorf("expected output to contain first message text, got %q", output)
	}
	if !strings.Contains(output, "[Using tool: Bash | pwd]") {
		t.Errorf("expected output to contain tool use with command detail, got %q", output)
	}
	if !strings.Contains(output, "You are in the /home/user/project directory.") {
		t.Errorf("expected output to contain second message text, got %q", output)
	}
}

func TestParseJSONStream_SkipsCompleteWhenDeltasPresent(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Dedup",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}
{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Hello world"}],"stop_reason":"end_turn"}
{"role":"system","cost_usd":0.005,"duration_ms":2000}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "Hello world"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_CompleteMessageRoleOnly(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Role-Only Messages",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"role":"assistant","content":[{"type":"text","text":"Here is the answer."}]}
{"role":"system","cost_usd":0.01,"duration_ms":3000}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "Here is the answer."
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_IgnoresInvalidJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task for Invalid JSON",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Valid text"}}
invalid json line
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" more text"}}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream should not fail on invalid JSON, got: %v", err)
	}

	output := sw.String()
	expected := "Valid text more text"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_CLIAssistantWrapper(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test CLI Assistant Wrapper",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"model":"claude-opus-4-6","type":"message","role":"assistant","content":[{"type":"text","text":"hello world"}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"hello world","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "hello world"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_CLIAssistantPartialMessages(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test CLI Partial Messages",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"He"}],"stop_reason":null}}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":null}}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Hello wor"}],"stop_reason":null}}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Hello world!"}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"Hello world!","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "Hello world!"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_InputJsonDeltaDoesNotSkipText(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Input JSON Delta",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"Read","input":{}}}
{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file"}}
{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"_path\":\"/path/to/file.go\"}"}}
{"type":"content_block_stop","index":0}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"tool_use","name":"Read","id":"toolu_123"},{"type":"text","text":"I've read the file and found the issue."}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"I've read the file and found the issue.","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if !strings.Contains(output, "[Using tool: Read]") {
		t.Errorf("expected output to contain tool use notification, got %q", output)
	}

	if !strings.Contains(output, "I've read the file and found the issue.") {
		t.Errorf("expected output to contain assistant text, got %q", output)
	}
}

func TestParseJSONStream_ResultFallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Result Fallback",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"result","subtype":"success","result":"The answer is 42","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "The answer is 42"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_ResultSkippedWhenOutputExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Result Not Duplicated",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"hello","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "hello"
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_CLIAssistantWithToolUse(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test CLI Assistant Tool Use",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Let me check that."},{"type":"tool_use","name":"Bash","id":"tool_123"}],"stop_reason":"tool_use"}}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Let me check that."},{"type":"tool_use","name":"Bash","id":"tool_123"},{"type":"text","text":" The result is 42."}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"Let me check that.\n[Using tool: Bash]\n The result is 42.","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()
	expected := "Let me check that.\n[Using tool: Bash]\n The result is 42."
	if output != expected {
		t.Errorf("expected output %q, got %q", expected, output)
	}
}

func TestParseJSONStream_MultiTurnAgenticLoop(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Multi-Turn Agentic",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"id":"msg_001","type":"message","role":"assistant","content":[{"type":"text","text":"I'll read the file."},{"type":"tool_use","name":"Read","id":"toolu_001"}],"stop_reason":"tool_use"}}
{"type":"assistant","message":{"id":"msg_002","type":"message","role":"assistant","content":[{"type":"text","text":"Now I'll fix the bug."},{"type":"tool_use","name":"Edit","id":"toolu_002"}],"stop_reason":"tool_use"}}
{"type":"assistant","message":{"id":"msg_003","type":"message","role":"assistant","content":[{"type":"text","text":"Done! The bug is fixed."}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"Done! The bug is fixed.","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if !strings.Contains(output, "I'll read the file.") {
		t.Errorf("expected turn 1 text, got %q", output)
	}
	if !strings.Contains(output, "[Using tool: Read]") {
		t.Errorf("expected turn 1 tool use, got %q", output)
	}
	if !strings.Contains(output, "Now I'll fix the bug.") {
		t.Errorf("expected turn 2 text, got %q", output)
	}
	if !strings.Contains(output, "[Using tool: Edit]") {
		t.Errorf("expected turn 2 tool use, got %q", output)
	}
	if !strings.Contains(output, "Done! The bug is fixed.") {
		t.Errorf("expected turn 3 text, got %q", output)
	}
}

func TestParseJSONStream_ThinkingDelta(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Thinking Delta",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me analyze this problem step by step..."}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" First, I need to check the code."}}
{"type":"content_block_stop","index":0}
{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is the answer."}}
{"type":"content_block_stop","index":1}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if !strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to contain [Thinking] header, got %q", output)
	}

	if !strings.Contains(output, "Let me analyze this problem step by step...") {
		t.Errorf("expected output to contain thinking content, got %q", output)
	}
	if !strings.Contains(output, "First, I need to check the code.") {
		t.Errorf("expected output to contain second thinking delta, got %q", output)
	}

	if !strings.Contains(output, "[/Thinking]") {
		t.Errorf("expected output to contain [/Thinking] end marker, got %q", output)
	}

	if !strings.Contains(output, "Here is the answer.") {
		t.Errorf("expected output to contain text response, got %q", output)
	}

	textOnly := sw.TextString()
	if strings.Contains(textOnly, "[Thinking]") {
		t.Errorf("expected TextString() to NOT contain [Thinking], got %q", textOnly)
	}
	if strings.Contains(textOnly, "analyze this problem") {
		t.Errorf("expected TextString() to NOT contain thinking content, got %q", textOnly)
	}
	if textOnly != "Here is the answer." {
		t.Errorf("expected TextString()=%q, got %q", "Here is the answer.", textOnly)
	}
}

func TestParseJSONStream_ThinkingInAssistantWrapper(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Thinking in Wrapper",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"id":"msg_001","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"I need to think about this carefully."},{"type":"text","text":"The answer is 42."}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"The answer is 42.","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if !strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to contain [Thinking] header, got %q", output)
	}
	if !strings.Contains(output, "I need to think about this carefully.") {
		t.Errorf("expected output to contain thinking text, got %q", output)
	}

	if !strings.Contains(output, "The answer is 42.") {
		t.Errorf("expected output to contain text response, got %q", output)
	}
}

func TestParseJSONStream_SkipThinkingInAssistantWrapper(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Skip Thinking in Wrapper",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"id":"msg_001","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"I need to think about creating a task. The user wants me to output [CREATE_TASK] markers."},{"type":"text","text":"I'll create that task for you.\n\n[CREATE_TASK]\n{\"title\": \"Task chaining\", \"prompt\": \"Implement task chaining\", \"category\": \"backlog\"}\n[/CREATE_TASK]"}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"success","result":"I'll create that task for you.\n\n[CREATE_TASK]\n{\"title\": \"Task chaining\", \"prompt\": \"Implement task chaining\", \"category\": \"backlog\"}\n[/CREATE_TASK]","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, true); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to NOT contain [Thinking] header when skipThinking=true, got %q", output)
	}
	if strings.Contains(output, "I need to think about creating a task") {
		t.Errorf("expected output to NOT contain thinking content when skipThinking=true, got %q", output)
	}

	if !strings.Contains(output, "I'll create that task for you.") {
		t.Errorf("expected output to contain text response, got %q", output)
	}
	if !strings.Contains(output, "[CREATE_TASK]") {
		t.Errorf("expected output to contain [CREATE_TASK] marker, got %q", output)
	}
}

func TestParseJSONStream_SkipThinkingInRoleOnlyMessage(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Skip Thinking Role-Only",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"role":"assistant","content":[{"type":"thinking","thinking":"Let me reason about this."},{"type":"text","text":"Here is the answer."}]}
{"role":"system","cost_usd":0.01,"duration_ms":3000}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, true); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to NOT contain [Thinking] header, got %q", output)
	}
	if strings.Contains(output, "Let me reason about this") {
		t.Errorf("expected output to NOT contain thinking content, got %q", output)
	}

	if !strings.Contains(output, "Here is the answer.") {
		t.Errorf("expected output to contain text response, got %q", output)
	}
}

func TestParseJSONStream_SkipThinkingForChat(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Skip Thinking Chat",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me analyze this problem step by step..."}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" First, I need to check the code."}}
{"type":"content_block_stop","index":0}
{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is the answer."}}
{"type":"content_block_stop","index":1}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, true); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to NOT contain [Thinking] header when skipThinking=true, got %q", output)
	}
	if strings.Contains(output, "[/Thinking]") {
		t.Errorf("expected output to NOT contain [/Thinking] end marker when skipThinking=true, got %q", output)
	}
	if strings.Contains(output, "Let me analyze this problem step by step") {
		t.Errorf("expected output to NOT contain thinking content when skipThinking=true, got %q", output)
	}

	if !strings.Contains(output, "Here is the answer.") {
		t.Errorf("expected output to contain text response, got %q", output)
	}
}

func TestParseJSONStream_SkipThinkingResultFallback(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Skip Thinking Result Fallback",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Thinking about this..."}}
{"type":"content_block_stop","index":0}
{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_1","name":"Read"}}
{"type":"content_block_stop","index":1}
{"type":"result","subtype":"success","result":"The file contains important data.","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, true); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	if strings.Contains(output, "[Thinking]") {
		t.Errorf("expected output to NOT contain [Thinking] header, got %q", output)
	}
	if strings.Contains(output, "Thinking about this") {
		t.Errorf("expected output to NOT contain thinking content, got %q", output)
	}

	if !strings.Contains(output, "[Using tool: Read]") {
		t.Errorf("expected output to contain tool use marker, got %q", output)
	}

	if !strings.Contains(output, "The file contains important data.") {
		t.Errorf("expected output to contain result fallback text, got %q", output)
	}
}

func TestParseCodexJSONStream_ExtractsTextAndToolOutput(t *testing.T) {
	sw := NewWriter("", "", nil, context.Background(), time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"item.started","item":{"type":"command_execution","command":"pwd"}}
{"type":"item.completed","item":{"type":"command_execution","stdout":"/tmp/project\n","exit_code":0}}
{"type":"item.completed","item":{"type":"agent_message","text":"All done."}}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseCodexJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseCodexJSONStream failed: %v", err)
	}

	output := sw.String()
	if !strings.Contains(output, "[Using tool: Bash]") {
		t.Errorf("expected output to contain tool marker, got %q", output)
	}
	if !strings.Contains(output, "$ pwd") {
		t.Errorf("expected output to contain command text, got %q", output)
	}
	if !strings.Contains(output, "/tmp/project") {
		t.Errorf("expected output to contain command stdout, got %q", output)
	}
	if !strings.Contains(output, "All done.") {
		t.Errorf("expected output to contain assistant text, got %q", output)
	}

	textOnly := sw.TextString()
	if textOnly != "All done." {
		t.Errorf("expected text-only output %q, got %q", "All done.", textOnly)
	}
}

func TestParseCodexJSONStream_TurnFailedSetsError(t *testing.T) {
	sw := NewWriter("", "", nil, context.Background(), time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"turn.failed","error":{"type":"network_error","message":"request failed"}}`
	reader := bytes.NewBufferString(jsonStream)
	if err := ParseCodexJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseCodexJSONStream failed: %v", err)
	}

	if !sw.IsError() {
		t.Fatal("expected writer to be marked as error")
	}
	if sw.ResultSubtype() != "network_error" {
		t.Errorf("expected subtype %q, got %q", "network_error", sw.ResultSubtype())
	}
	if !strings.Contains(sw.String(), "request failed") {
		t.Errorf("expected output to include failure message, got %q", sw.String())
	}
}

func TestParseJSONStream_CapturesIsError(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test IsError Capture",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"Working on it..."}],"stop_reason":"end_turn"}}
{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Max turns reached","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	if !sw.IsError() {
		t.Error("expected IsError() to be true after is_error result event")
	}
	if sw.ResultSubtype() != "error_max_turns" {
		t.Errorf("expected ResultSubtype()=%q, got %q", "error_max_turns", sw.ResultSubtype())
	}
}

func TestParseJSONStream_IsErrorFalseByDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test IsError Default",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"result","subtype":"success","result":"All done","session_id":"abc123"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	if sw.IsError() {
		t.Error("expected IsError() to be false for successful result")
	}
	if sw.ResultSubtype() != "" {
		t.Errorf("expected empty ResultSubtype(), got %q", sw.ResultSubtype())
	}
}

func TestParseJSONStream_UnclosedThinkingBlocks(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Unclosed Thinking",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me analyze this."}}
{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is my response."}}
{"type":"content_block_stop","index":1}
{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"Read"}}
{"type":"content_block_stop","index":0}
{"type":"message_stop"}
{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Now let me continue."}}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	thinkingCount := strings.Count(output, "[Thinking]")
	closeCount := strings.Count(output, "[/Thinking]")
	if thinkingCount != closeCount {
		t.Errorf("expected balanced thinking markers: [Thinking]=%d, [/Thinking]=%d, output=%q",
			thinkingCount, closeCount, output)
	}

	if thinkingCount != 2 {
		t.Errorf("expected 2 [Thinking] markers, got %d, output=%q", thinkingCount, output)
	}

	if !strings.Contains(output, "Here is my response.") {
		t.Errorf("expected text response in output, got %q", output)
	}

	if !strings.Contains(output, "Let me analyze this.") {
		t.Errorf("expected first thinking content, got %q", output)
	}
	if !strings.Contains(output, "Now let me continue.") {
		t.Errorf("expected second thinking content, got %q", output)
	}

	if !strings.Contains(output, "[Using tool: Read]") {
		t.Errorf("expected tool use marker, got %q", output)
	}
}

func TestParseJSONStream_MessageStopClosesThinking(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test MessageStop Thinking",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 1*time.Hour)
	defer sw.Stop()

	jsonStream := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Turn 1 thinking."}}
{"type":"message_stop"}
{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Turn 2 thinking."}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Final answer."}}
{"type":"message_stop"}
`

	reader := bytes.NewBufferString(jsonStream)
	if err := ParseJSONStream(reader, sw, false); err != nil {
		t.Fatalf("parseJSONStream failed: %v", err)
	}

	output := sw.String()

	thinkingCount := strings.Count(output, "[Thinking]")
	closeCount := strings.Count(output, "[/Thinking]")
	if thinkingCount != closeCount {
		t.Errorf("expected balanced thinking markers: [Thinking]=%d, [/Thinking]=%d, output=%q",
			thinkingCount, closeCount, output)
	}

	if !strings.Contains(output, "Turn 1 thinking.") {
		t.Errorf("expected turn 1 thinking, got %q", output)
	}
	if !strings.Contains(output, "Turn 2 thinking.") {
		t.Errorf("expected turn 2 thinking, got %q", output)
	}
}

func TestExtractToolDetail_LongBashPreservesLaterContext(t *testing.T) {
	inputJSON := `{"command":"cd /Users/dubee/go/src/github.com/openvibely/openvibely/.worktrees/task_6a40e9f8fefa53ac8d203aa3fd3a70be && rg -n \"extractToolDetail|task thread|chat_shared.templ\" internal pkg web/templates/components/chat_shared.templ"}`

	got := extractToolDetail("Bash", inputJSON)
	if !strings.Contains(got, "chat_shared.templ") {
		t.Fatalf("expected later command context to survive truncation, got %q", got)
	}
}
