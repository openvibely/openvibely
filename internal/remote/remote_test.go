package remote

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

const server = "0123456789abcdef"

func TestTagger(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	p := &models.Project{ID: "default", Name: "Default " + hex}
	task := &models.Task{ID: hex, ProjectID: models.AllProjectsID, Title: "fix " + hex}
	Tag(server, p)
	Tag(server, task)
	if p.ID != remoteid.ID(server, "default") || p.Name != "Default "+hex {
		t.Fatalf("project = %q %q", p.ID, p.Name)
	}
	if task.ID != remoteid.ID(server, hex) || task.ProjectID != models.AllProjectsID || task.Title != "fix "+hex {
		t.Fatalf("task = %q %q %q", task.ID, task.ProjectID, task.Title)
	}
}

func TestTaggerLeavesTextUnchanged(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	text := `see /tasks/` + hex + `?project_id=` + hex + ` {"task_id":"` + hex + `","ids":["` + hex + `"]}`
	task := &models.Task{ID: hex, Prompt: text, Title: `{"task_id":"` + hex + `"}`}
	Tag(server, task)
	if task.Prompt != text || task.Title != `{"task_id":"`+hex+`"}` {
		t.Fatalf("text rewritten: %q %q", task.Prompt, task.Title)
	}
}

func TestTaggerTagsJSONSettingsIDs(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	task := &models.Task{ChainConfig: `{"child_agent_id":"` + hex + `","child_prompt_prefix":"see ` + hex + `","child_chain_config":{"child_task_id":"` + hex + `"}}`}
	node := &models.AutomationNode{ConfigJSON: `{"agent_ids":["` + hex + `"],"timeout":1.50}`}
	Tag(server, task)
	Tag(server, node)
	id := remoteid.ID(server, hex)
	if want := `{"child_agent_id":"` + id + `","child_chain_config":{"child_task_id":"` + id + `"},"child_prompt_prefix":"see ` + hex + `"}`; task.ChainConfig != want {
		t.Fatalf("ChainConfig = %s, want %s", task.ChainConfig, want)
	}
	if want := `{"agent_ids":["` + id + `"],"timeout":1.50}`; node.ConfigJSON != want {
		t.Fatalf("ConfigJSON = %s, want %s", node.ConfigJSON, want)
	}
	if got := tagJSON(server, `not json `+hex); got != `not json `+hex {
		t.Fatalf("non-JSON rewritten: %s", got)
	}
}

func TestArgServer(t *testing.T) {
	other := "fedcba9876543210"
	if s, mixed := argServer([]any{remoteid.ID(server, "a"), "b"}); s != server || mixed {
		t.Fatalf("argServer = %q %v", s, mixed)
	}
	if _, mixed := argServer([]any{remoteid.ID(server, "a"), remoteid.ID(other, "b")}); !mixed {
		t.Fatal("mixed servers must be reported")
	}
}

func TestPrepareArgsStripsAndReplacesUser(t *testing.T) {
	paramNames["Test"] = map[string][]string{"GetThing": {"userID", "projectID"}}
	defer delete(paramNames, "Test")
	payload, err := prepareArgs("Test", "GetThing", server, []any{"local-user", remoteid.ID(server, "p1")}, false)
	if err != nil {
		t.Fatal(err)
	}
	values, err := decodeValues(payload, []reflect.Type{reflect.TypeOf(""), reflect.TypeOf("")})
	if err != nil {
		t.Fatal(err)
	}
	if values[0].String() != Self || values[1].String() != "p1" {
		t.Fatalf("args = %q %q", values[0].String(), values[1].String())
	}
}

type roleStore struct {
	persistence.ProjectAccessStore
	roles map[string]string
}

func (s roleStore) Role(_ context.Context, _ string, projectID string) (string, error) {
	return s.roles[projectID], nil
}

func TestExecutorAuthorize(t *testing.T) {
	e := &Executor{Authorizer: &authz.Authorizer{Store: roleStore{roles: map[string]string{"ok": "viewer"}}}}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: "u"})
	owners := func(ids ...string) ownerSet {
		o := ownerSet{projects: map[string]bool{}, tasks: map[string]bool{}, execs: map[string]bool{}}
		for _, id := range ids {
			o.projects[id] = true
		}
		return o
	}
	if err := e.authorize(ctx, owners("ok"), map[string]error{}); err != nil {
		t.Fatalf("viewer denied: %v", err)
	}
	if err := e.authorize(ctx, owners("ok", "secret"), map[string]error{}); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("unshared project allowed: %v", err)
	}
	if err := e.authorize(context.Background(), owners(models.AllProjectsID), map[string]error{}); err == nil {
		t.Fatal("all projects without a principal allowed")
	}
}

func TestExecutorRejectsWritesAndUnknownStores(t *testing.T) {
	e := &Executor{}
	for _, read := range []Read{{Store: "Tasks", Method: "Create"}, {Store: "Settings", Method: "Get"}, {Store: "ProjectAccess", Method: "ListMembers"}} {
		if _, err := e.run(context.Background(), read); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s.%s: err = %v", read.Store, read.Method, err)
		}
	}
}

func TestScrubValueClearsSecrets(t *testing.T) {
	cfg := models.LLMConfig{Name: "gpt", BaseURL: "https://llm.example", APIKey: "k", ExtraHeadersJSON: `{"Authorization":"Bearer x"}`, CustomAuthConfigJSON: "c", CustomAuthStateJSON: "s", OAuthClientSecret: "cs"}
	gh := models.GitHubConnection{PAT: "ghp_x", Login: "me"}
	cfgs := []models.LLMConfig{cfg}
	for _, v := range []any{&cfg, &gh, &cfgs} {
		scrubValue(reflect.ValueOf(v).Elem(), nil)
	}
	for _, c := range []models.LLMConfig{cfg, cfgs[0]} {
		if c.APIKey != "" || c.ExtraHeadersJSON != "" || c.CustomAuthConfigJSON != "" || c.CustomAuthStateJSON != "" || c.OAuthClientSecret != "" {
			t.Fatalf("secrets kept: %+v", c)
		}
		if c.Name != "gpt" || c.BaseURL != "https://llm.example" {
			t.Fatalf("plain fields cleared: %+v", c)
		}
	}
	if gh.PAT != "" || gh.Login != "me" {
		t.Fatalf("github = %+v", gh)
	}
}

// unwrappedMethods are concrete store methods no caller reaches through a type
// assertion on the wrapped stores. Add a pass-through in optional.go instead of
// listing a method here when code type-asserts for it.
var unwrappedMethods = map[string]string{
	"Agents":               "GetByIDs List ListPickerOptions ListSelectableByName",
	"AgentMutations":       "FindAppliedByIdempotencyKey ListForExecution ListForTask",
	"Alerts":               "SetAutomationRepo",
	"Attachments":          "DeleteByTask GetAllFilePaths",
	"Automations":          "AbandonQueuedDispatch BindThreadInput BindingsForWorkItemKey FinalizeExecutionProjection FindActivityResource GetCustomTaskHandoff ListByProject ListPortfolioCardsPage ListSavedByProject PublishResourceInvalidations RenewDispatchLease SetBroadcaster",
	"ChannelTargets":       "Delete DeleteForProject DeleteProjectExcept ListSendsByProject Upsert",
	"ChatAttachments":      "GetAllFilePaths GetByID",
	"DiscordAuth":          "Delete HasAnyAuthorizedUsers HasAnyAuthorizedUsersAnywhere IsAuthorizedForProject",
	"DiscordTaskContexts":  "DeleteByTaskID UpsertInTx",
	"DiscordUserProjects":  "DeleteUserProject",
	"EmailAuth":            "Delete HasAnyAuthorizedUsers HasAnyAuthorizedUsersAnywhere IsAuthorizedForProject",
	"EmailSenderProjects":  "DeleteSenderProject",
	"EmailTaskContexts":    "DeleteByTaskID UpsertInTx",
	"Executions":           "GetFailedTaskPatterns ListByProject ListByTaskIDs ListTaskGeminiHistory ListTaskAnthropicHistory ListTaskCompletionsHistory ListTaskResponsesHistory SetAutomationRepo",
	"GitHubPRFeedback":     "RecordForwarded",
	"Insights":             "GetKnowledge",
	"Lifecycle":            "HasNewerTaskRun ListExecutionEvents ListExecutionsForTask",
	"LLMConfigs":           "Count LinkOAuthConnection ListCards ListCardsPage UpdateCustomOAuthConnection UpdateOAuthTokens",
	"OpenAIAsyncToolCalls": "CancelByExecution GetByExecutionCallID ListRecoverable",
	"Projects":             "Delete ListRepoRoots",
	"ReviewComments":       "CountByTask",
	"Schedules":            "ClaimCalendarOccurrence ListByTaskIDs MarkRan ReleaseCalendarOccurrence SuppressedOccurrence ToggleEnabled",
	"SkillAnalytics":       "GetAgentUsage GetEvidence GetSelectionFollowThrough GetTopSkills GetUnderusedSkills GetUsageOverTime",
	"SlackAuth":            "Delete HasAnyAuthorizedUsers HasAnyAuthorizedUsersAnywhere IsAuthorizedForProject",
	"SlackTaskContexts":    "DeleteByTaskID",
	"SlackUserProjects":    "DeleteUserProject",
	"TaskCommitStats":      "ListProducedCommitStats",
	"TaskGoals":            "Delete",
	"TaskPullRequests":     "GetByIssueNumber SetBroadcaster",
	"Tasks":                "CountRunningByProject CountRunningTotal CreateWithGoal DeleteAllBacklog DeleteAllChat DeleteAllCompleted ListByCategory ListByProjectWithSort SetExecutionRepo UpdateLineage",
	"TelegramAuth":         "Delete HasAnyAuthorizedUsers",
	"TelegramUserProjects": "DeleteUserProject",
	"ThreadInputs":         "ClaimQueuedForTaskExecution ListPendingSteering ListRecoverableQueuedTaskIDs RecordProviderSteering",
	"Webhooks":             "Delete GetTaskAgentAssignments ListByProject ListCardsByProjectPage",
}

func TestWrappersKeepConcreteStoreMethods(t *testing.T) {
	local := teststores.New(t).Stores
	lv, wv := reflect.ValueOf(local), reflect.ValueOf(Wrap(local, &Router{}))
	for i := 0; i < lv.NumField(); i++ {
		l, w := lv.Field(i), wv.Field(i)
		if l.Kind() != reflect.Interface || l.IsNil() || w.Elem().Type() == l.Elem().Type() {
			continue
		}
		name := lv.Type().Field(i).Name
		skip := map[string]bool{}
		for _, m := range strings.Fields(unwrappedMethods[name]) {
			skip[m] = true
		}
		for j := 0; j < l.Elem().Type().NumMethod(); j++ {
			m := l.Elem().Type().Method(j).Name
			if _, ok := w.Elem().Type().MethodByName(m); !ok && !skip[m] {
				t.Errorf("remote wrapper for %s hides %s; add a pass-through in optional.go", name, m)
			}
		}
	}
}

func TestWrappersSatisfyTypeAssertedFeatures(t *testing.T) {
	s := Wrap(teststores.New(t).Stores, &Router{})
	checks := map[string]bool{}
	_, checks["lifecycle trace"] = s.Lifecycle.(interface {
		AppendExecutionEvent(context.Context, *models.LifecycleExecutionEvent) error
	})
	_, checks["checkpoint cache"] = s.Executions.(interface {
		GetChatCompactionCheckpointIfChanged(context.Context, string, string, string) (*models.ChatCompactionCheckpoint, string, error)
	})
	_, checks["email direct send"] = s.EmailAuth.(interface {
		IsAuthorizedAnywhere(context.Context, string) (bool, error)
	})
	for name, store := range map[string]any{"telegram": s.TelegramAuth, "slack": s.SlackAuth, "discord": s.DiscordAuth, "email": s.EmailAuth} {
		_, checks[name+" bind sender"] = store.(interface {
			BindSender(context.Context, string, string, string) error
		})
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s unavailable through wrapped stores", name)
		}
	}
}

func TestPublishTagsSocketOwner(t *testing.T) {
	var task events.TaskEvent
	var chat events.ChatEvent
	c := &Conn{hub: &Hub{OnTask: func(e events.TaskEvent) { task = e }, OnChat: func(e events.ChatEvent) { chat = e }}, userID: "alice", serverID: server}
	const checksum = "0123456789abcdef0123456789abcdef"
	c.publish(Frame{Event: "task", Data: []byte(`{"type":"task_updated","project_id":"p1","message":"` + checksum + `"}`)})
	c.publish(Frame{Event: "chat_done", Data: []byte(`{"type":"chat_done","project_id":"p1","completed_output":"` + checksum + `"}`)})
	if task.Message != checksum || chat.CompletedOutput != checksum {
		t.Fatalf("event text was rewritten: task=%q chat=%q", task.Message, chat.CompletedOutput)
	}
	if task.RemoteUserID != "alice" || task.ProjectID != remoteid.ID(server, "p1") {
		t.Fatalf("task = %+v", task)
	}
	if chat.RemoteUserID != "alice" || chat.ProjectID != remoteid.ID(server, "p1") {
		t.Fatalf("chat = %+v", chat)
	}
}

func TestActionServerRoutesOnIDsOnly(t *testing.T) {
	other := "fedcba9876543210"
	type Ref struct{ TaskID, ProjectID string }
	type args struct {
		Ref
		Prompt  string
		FileIDs []string
	}
	got, err := ActionServer(args{Prompt: "see " + remoteid.ID(server, "x")})
	if err != nil || got != "" {
		t.Fatalf("free text routed the action: %q %v", got, err)
	}
	got, err = ActionServer(args{Ref: Ref{TaskID: remoteid.ID(server, "t")}})
	if err != nil || got != server {
		t.Fatalf("got %q %v, want %s", got, err, server)
	}
	got, err = ActionServer(remoteid.ID(server, "t"))
	if err != nil || got != server {
		t.Fatalf("bare ID: got %q %v", got, err)
	}
	if _, err := ActionServer(args{Ref: Ref{TaskID: remoteid.ID(server, "t")}, FileIDs: []string{remoteid.ID(other, "f")}}); err == nil {
		t.Fatal("mixed servers should fail")
	}
}

func TestRawFieldsAreNotRewritten(t *testing.T) {
	type chunk struct {
		TaskID string
		Data   []byte `remote:"raw"`
		Name   string `remote:"raw"`
	}
	id := remoteid.ID(server, "t")
	c := chunk{TaskID: "t", Data: []byte(id), Name: "local"}
	Tag(server, &c)
	if c.TaskID != id || string(c.Data) != id || c.Name != "local" {
		t.Fatalf("unexpected tagging: %+v", c)
	}
}

type ownerlessTasks struct {
	persistence.TaskStore
	rows []models.Task
}

func (s ownerlessTasks) CountPendingByProject(context.Context) (map[string]int, error) {
	return map[string]int{"ok": 3, "secret": 9}, nil
}

func (s ownerlessTasks) ListWithWorktrees(context.Context) ([]models.Task, error) { return s.rows, nil }

type instanceAgents struct{ persistence.AgentStore }

func (instanceAgents) ListIncludingArchived(context.Context) ([]models.Agent, error) {
	return []models.Agent{{ID: "a1", Name: "Coder"}}, nil
}

func TestExecutorRequiresOwnerForProjectData(t *testing.T) {
	tasks := ownerlessTasks{rows: []models.Task{{ID: "t1", ProjectID: "ok"}, {ID: "t2"}, {ID: "t3", ProjectID: "secret"}}}
	e := &Executor{
		Stores:     persistence.Stores{Tasks: tasks, Agents: instanceAgents{}},
		Authorizer: &authz.Authorizer{Store: roleStore{roles: map[string]string{"ok": "viewer"}}},
	}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: "u"})
	args, err := encodeValues(nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.run(ctx, Read{Store: "Tasks", Method: "CountPendingByProject", Args: args}); !errors.Is(err, persistence.ErrProjectDenied) {
		t.Fatalf("ownerless project data: err = %v", err)
	}

	out, err := e.run(ctx, Read{Store: "Agents", Method: "ListIncludingArchived", Args: args})
	if err != nil {
		t.Fatalf("instance-wide store refused: %v", err)
	}
	if agents := out[0].Interface().([]models.Agent); len(agents) != 1 || agents[0].Name != "Coder" {
		t.Fatalf("agents = %+v", agents)
	}

	out, err = e.run(ctx, Read{Store: "Tasks", Method: "ListWithWorktrees", Args: args})
	if err != nil {
		t.Fatalf("list refused: %v", err)
	}
	if rows := out[0].Interface().([]models.Task); len(rows) != 1 || rows[0].ID != "t1" {
		t.Fatalf("rows = %+v, want only t1", rows)
	}
}

type webhookRows struct {
	persistence.WebhookStore
	rows []models.WebhookEndpoint
}

func (s webhookRows) ListCardsByProject(_ context.Context, projectID string) ([]models.WebhookEndpoint, error) {
	return s.rows, nil
}

func TestWebhookCredentialsNeedManage(t *testing.T) {
	rows := []models.WebhookEndpoint{{ID: "w", ProjectID: "p", PathToken: "tok", Secret: "sec"}}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: "u"})
	args, err := encodeValues([]reflect.Value{reflect.ValueOf("p")})
	if err != nil {
		t.Fatal(err)
	}
	for role, keep := range map[string]bool{"editor": false, "maintainer": true} {
		e := &Executor{
			Stores:     persistence.Stores{Webhooks: webhookRows{rows: rows}},
			Authorizer: &authz.Authorizer{Store: roleStore{roles: map[string]string{"p": role}}},
		}
		out, err := e.run(ctx, Read{Store: "Webhooks", Method: "ListCardsByProject", Args: args})
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		w := out[0].Interface().([]models.WebhookEndpoint)[0]
		if got := w.PathToken == "tok" && w.Secret == "sec"; got != keep {
			t.Fatalf("%s: token=%q secret=%q, want kept=%v", role, w.PathToken, w.Secret, keep)
		}
		if w.Name != rows[0].Name || w.ID != "w" {
			t.Fatalf("%s: other fields changed: %+v", role, w)
		}
	}
	if rows[0].PathToken != "tok" {
		t.Fatal("scrub modified the store's rows")
	}
	tagged := rows[0]
	Tag(server, &tagged)
	if tagged.PathToken != "tok" || tagged.Secret != "sec" {
		t.Fatalf("tokens rewritten: %+v", tagged)
	}
}

type llmRows struct{ persistence.LLMConfigStore }

func (llmRows) List(context.Context) ([]models.LLMConfig, error) {
	return []models.LLMConfig{{ID: "m", Name: "gpt", APIKey: "sk-live"}}, nil
}

func TestExecutorScrubsListResults(t *testing.T) {
	e := &Executor{Stores: persistence.Stores{LLMConfigs: llmRows{}}, Authorizer: &authz.Authorizer{Store: roleStore{}}}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: "u"})
	args, _ := encodeValues(nil)
	out, err := e.run(ctx, Read{Store: "LLMConfigs", Method: "List", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if cfg := out[0].Interface().([]models.LLMConfig)[0]; cfg.APIKey != "" || cfg.Name != "gpt" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestTaggerPreservesBareHexContent(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	task := models.Task{ID: hex, ProjectID: "default", Title: hex, Prompt: hex}
	execution := models.Execution{ID: hex, TaskID: hex, Output: hex, DiffOutput: hex}
	Tag(server, &task)
	Tag(server, &execution)
	if task.Title != hex || task.Prompt != hex || execution.Output != hex || execution.DiffOutput != hex {
		t.Fatalf("content rewritten: task=%+v execution=%+v", task, execution)
	}
	if task.ID != remoteid.ID(server, hex) || task.ProjectID != remoteid.ID(server, "default") || execution.TaskID != task.ID {
		t.Fatal("entity references were not tagged")
	}
	bytes := []byte(hex)
	Tag(server, &bytes)
	if string(bytes) != hex {
		t.Fatal("file bytes rewritten")
	}
	text := map[string]string{hex: hex}
	Tag(server, &text)
	if text[hex] != hex {
		t.Fatal("ordinary map text rewritten")
	}
}

func TestReadResultsDistinguishTextFromIDs(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	diff := hex
	tagReadResult(server, "Executions", "GetLatestNonEmptyDiffOutput", 0, reflect.ValueOf(&diff).Elem())
	if diff != hex {
		t.Fatal("diff text rewritten")
	}
	taskID := hex
	tagReadResult(server, "Executions", "GetExecutionTaskID", 0, reflect.ValueOf(&taskID).Elem())
	if taskID != remoteid.ID(server, hex) {
		t.Fatal("bare task ID not tagged")
	}
	ids := []string{hex, "default"}
	tagReadResult(server, "Alerts", "ListOwnerProjectIDs", 0, reflect.ValueOf(&ids).Elem())
	if ids[0] != remoteid.ID(server, hex) || ids[1] != remoteid.ID(server, "default") {
		t.Fatalf("project IDs not tagged: %v", ids)
	}
	modelsByID := map[string]*models.LLMConfig{hex: {ID: hex, Name: hex}}
	tagReadResult(server, "LLMConfigs", "GetByIDs", 0, reflect.ValueOf(&modelsByID).Elem())
	config := modelsByID[remoteid.ID(server, hex)]
	if config == nil || config.ID != remoteid.ID(server, hex) || config.Name != hex {
		t.Fatalf("model map corrupted: %+v", modelsByID)
	}
}
