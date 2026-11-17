package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/pkg/agenttools"
)

type ProjectComputerService struct {
	readinessLookups    *singleflight.Group
	broadcaster         *events.Broadcaster
	onFilePublished     func(context.Context, string, string)
	verifyLoginVisual   func(context.Context, string, string, string, []byte) (bool, error)
	attachmentDirectory string
	repo                persistence.ProjectComputerStore
	projects            persistence.ProjectStore
	provisioner         ComputerProvisioner
	client              *http.Client
	locks               *sync.Map
	active              *sync.Map
	taskID              string
}

func NewProjectComputerService(projects persistence.ProjectStore, p ComputerProvisioner) *ProjectComputerService {
	if p == nil {
		p = defaultComputerProvisioner()
	}
	return &ProjectComputerService{repo: projects.Computers(), readinessLookups: &singleflight.Group{}, locks: &sync.Map{}, active: &sync.Map{}, projects: projects, provisioner: p, client: &http.Client{Timeout: (agenttools.MaxBashTimeoutSeconds + 10) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SetBroadcaster wires status invalidations into the app's existing live transport.
// Configure before serving requests; task-scoped services share the broadcaster.
func (s *ProjectComputerService) SetBroadcaster(b *events.Broadcaster) { s.broadcaster = b }
func (s *ProjectComputerService) publishStatus(id string, allTasks bool) {
	if s.broadcaster == nil {
		return
	}
	taskID := s.taskID
	if allTasks {
		taskID = ""
	}
	// Only identifiers are broadcast. Status details still require the computer
	// endpoint's editor authorization, unlike ordinary task notifications.
	s.broadcaster.Publish(events.TaskEvent{Type: events.ComputerStatusChanged, ProjectID: id, TaskID: taskID})
}
func (s *ProjectComputerService) lock(id string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(id+":"+s.taskID, &sync.Mutex{})
	return m.(*sync.Mutex)
}
func (s *ProjectComputerService) lifecycle(id string) *sync.RWMutex {
	m, _ := s.locks.LoadOrStore("lifecycle:"+id, &sync.RWMutex{})
	return m.(*sync.RWMutex)
}
func (s *ProjectComputerService) Get(ctx context.Context, id string) (*models.ProjectComputer, error) {
	return s.repo.Get(ctx, id)
}
func (s *ProjectComputerService) Connection(ctx context.Context, id string) (ComputerConnection, error) {
	return s.connection(ctx, id, false)
}
func (s *ProjectComputerService) connection(ctx context.Context, id string, current bool) (ComputerConnection, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return ComputerConnection{}, err
	}
	if !c.Enabled {
		return ComputerConnection{}, errors.New("project computer is disabled")
	}
	if c.Provider != s.provisioner.Provider() {
		return ComputerConnection{}, errors.New("computer provider differs from this installation")
	}
	conn, err := s.resolveConnection(ctx, id, current)

	if err == nil && s.taskID != "" {
		conn.Endpoint += "/desktops/" + ComputerDesktopID(s.taskID)
		stale, cleanupErr := s.repo.ExpiredChromeInstallations(ctx, id)
		if cleanupErr != nil {
			return ComputerConnection{}, cleanupErr
		}
		for _, grant := range stale {
			body, _ := json.Marshal(map[string]any{"action": "clear_cookies", "cookies": grant.Selections})
			if _, cleanupErr = s.request(ctx, conn, "POST", "/browser", body); cleanupErr != nil {
				return ComputerConnection{}, errors.New("could not clear expired browser permissions")
			}
			if cleanupErr = s.repo.ClearChromeInstall(ctx, id, grant.ID); cleanupErr != nil {
				return ComputerConnection{}, cleanupErr
			}
		}
	}
	return conn, err
}

// Only concurrent readiness lookups share a result; nothing is cached afterward.
// Task authorization, cookie cleanup, and desktop health remain caller-specific.
func (s *ProjectComputerService) resolveConnection(ctx context.Context, id string, current bool) (ComputerConnection, error) {
	if !current || s.readinessLookups == nil {
		return s.resolveContainer(ctx, id, current)
	}
	result := s.readinessLookups.DoChan(id, func() (any, error) {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.resolveContainer(check, id, true)
	})
	select {
	case <-ctx.Done():
		return ComputerConnection{}, ctx.Err()
	case resolved := <-result:
		if resolved.Err != nil {
			return ComputerConnection{}, resolved.Err
		}
		return resolved.Val.(ComputerConnection), nil
	}
}
func (s *ProjectComputerService) resolveContainer(ctx context.Context, id string, current bool) (ComputerConnection, error) {
	var conn ComputerConnection
	var err error
	if resolver, ok := s.provisioner.(interface {
		ResolveCurrent(context.Context, string) (ComputerConnection, error)
	}); current && ok {
		conn, err = resolver.ResolveCurrent(ctx, id)
	} else {
		if checker, ok := s.provisioner.(interface {
			Current(context.Context, string) bool
		}); current && ok && !checker.Current(ctx, id) {
			return ComputerConnection{}, errors.New("project computer image is outdated")
		}
		conn, err = s.provisioner.Resolve(ctx, id)
	}

	return conn, err
}
func (s *ProjectComputerService) Start(ctx context.Context, id string) error {
	return s.start(ctx, id, false)
}
func (s *ProjectComputerService) EnsureStarted(ctx context.Context, id string) error {
	if s.taskID != "" {
		state, err := s.repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if !state.Enabled {
			return errors.New("computer use is disabled for this task or project")
		}
	}

	root := *s
	root.taskID = ""
	root.repo = s.projects.Computers()
	if root.Ready(ctx, id) {
		// Idle maintenance runs independently of tool calls. A ready computer
		// must not wait for unrelated desktops to shut down.
		return nil
	}
	return root.start(ctx, id, true)
}
func (s *ProjectComputerService) start(ctx context.Context, id string, automatic bool) error {
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	if s.taskID != "" {
		return errors.New("start the project computer")
	}
	if err := s.repo.Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return errors.New("project computer is disabled")
	}
	if automatic && s.Ready(ctx, id) {
		return nil
	}
	if busy, err := s.repo.SessionsBusy(ctx, id); err != nil {
		return err
	} else if busy && !automatic {
		return persistence.ErrComputerBusy
	}
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if project, err := s.projects.GetByID(ctx, id); err != nil || project == nil {
		return errors.New("project not found")
	}
	c, err := s.repo.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		c = &models.ProjectComputer{ProjectID: id, Provider: s.provisioner.Provider(), Enabled: true}
	} else if err != nil {
		return err
	}
	if c.Provider != s.provisioner.Provider() {
		return errors.New("computer provider differs from this installation")
	}
	if !automatic && c.Owner != "" && c.LeaseUntil >= time.Now().Unix() {
		return persistence.ErrComputerBusy
	}
	if checker, ok := s.provisioner.(interface{ CheckAvailable(context.Context) error }); ok {
		if err := checker.CheckAvailable(ctx); err != nil {
			return err
		}
	}
	defer s.publishStatus(id, true)
	if err := s.repo.MarkCleanupRequired(ctx, id); err != nil {
		return err
	}
	var conn ComputerConnection
	if local, ok := s.provisioner.(interface {
		StartWithTimeZone(context.Context, string, string) (ComputerConnection, error)
	}); ok {
		conn, err = local.StartWithTimeZone(ctx, id, c.TimeZone)
	} else {
		conn, err = s.provisioner.Start(ctx, id)
	}
	if err != nil {
		return err
	}
	if err := s.repo.Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	ready, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		data, healthErr := s.request(ready, conn, "GET", "/health", nil)
		err = healthErr
		if err == nil {
			if err := s.requireIsolation(data); err != nil {
				return err
			}

			return nil
		}
		select {
		case <-ready.Done():
			return errors.New("project computer did not become ready; check its container logs")
		case <-time.After(time.Second):
		}
	}
}
func (s *ProjectComputerService) Stop(ctx context.Context, id string) error {
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	return s.stopLocked(ctx, id)
}

// stopLocked requires the project lifecycle write lock. Project deletion holds
// it through the database deletion and resource cleanup, not just the stop.
func (s *ProjectComputerService) stopLocked(ctx context.Context, id string) error {
	if s.taskID != "" {
		return errors.New("stop the project computer in project settings")
	}
	if busy, err := s.repo.SessionsBusy(ctx, id); err != nil {
		return err
	} else if busy {
		return persistence.ErrComputerBusy
	}
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Owner != "" && c.LeaseUntil >= time.Now().Unix() {
		return persistence.ErrComputerBusy
	}
	if c.Provider != s.provisioner.Provider() {
		return errors.New("computer provider differs from this installation")
	}
	return s.changeViewerPermission(id, true, func() error { return s.provisioner.Stop(ctx, id) })
}

// checkViewerPermission is called once when registering a control connection.
func (s *ProjectComputerService) checkViewerPermission(ctx context.Context, id string) error {
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return errors.New("computer use is disabled")
	}
	if state.HandoffKind != "" && !state.Held {
		return persistence.ErrComputerBusy
	}
	return nil
}

// WithHumanInput serializes human file/workflow operations with agent actions.
// Private handoff verification remains blocked.
func (s *ProjectComputerService) WithHumanInput(ctx context.Context, id string, fn func() error) error {
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !c.Enabled {
		return errors.New("computer use is disabled")
	}
	if c.HandoffKind != "" && !c.Held {
		return persistence.ErrComputerBusy
	}
	return fn()
}
func (s *ProjectComputerService) request(ctx context.Context, conn ComputerConnection, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, conn.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("project computer is unavailable")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 12_000_001))
	if err != nil {
		return nil, err
	}
	if len(b) > 12_000_000 {
		return nil, errors.New("computer response too large")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("computer operation failed (%d)", res.StatusCode)
	}
	return b, nil
}
func (s *ProjectComputerService) Ready(ctx context.Context, id string) bool {
	conn, err := s.connection(ctx, id, true)
	if err != nil {
		return false
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := s.request(check, conn, "GET", "/health", nil)
	return err == nil && s.requireIsolation(data) == nil
}

func (s *ProjectComputerService) runtimeTools(ctx context.Context, projectID, owner string) *llmcontracts.RuntimeTools {
	if s == nil || projectID == "" || owner == "" {
		return nil
	}
	state, err := s.repo.Get(ctx, projectID)
	if err != nil || !state.Enabled {
		return nil
	}
	owner += ":" + persistence.NewID()
	executionID := llmcontracts.ArtifactExecutionIDFromContext(ctx)
	if executionID == "" {
		executionID = owner
	}
	var cancelExecutionOnce sync.Once
	defs := projectComputerToolDefinitions()
	routed := state.ToolTarget == "computer"
	if routed {
		for _, d := range agenttools.Definitions() {
			description := "Runs in this task computer, under Files. " + d.Description
			if d.Name == "shell" || d.Name == "process" {
				description = "Runs in this task computer under Files, using isolated Linux bash. " + d.Description
				description = strings.Split(description, " Selected shell:")[0]
				description = strings.Split(description, " Shell unavailable:")[0]
			}
			defs = append(defs, llmcontracts.RuntimeToolDefinition{Name: d.Name, Description: description, Schema: d.Schema, Access: llmcontracts.RuntimeToolAccessWrite})
		}
	}
	var releaseOnce sync.Once
	var reserved bool // Accessed under the desktop lock below.
	return &llmcontracts.RuntimeTools{Definitions: defs, SkipDefaultTools: routed, Executor: func(call context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		explicitProcess := name == "computer_shell" || name == "computer_job"
		processTool := ""
		switch name {
		case "computer_shell":
			processTool = "shell"
		case "computer_job":
			processTool = "process"
			name = "computer_shell"
		}
		if routed {
			switch name {
			case "bash", "Bash", "shell", "process":
				processTool = name
				name = "computer_shell"
			case "read_file", "write_file", "edit_file", "list_files", "grep_search":
				input, _ = json.Marshal(map[string]any{"operation": name, "input": json.RawMessage(input)})
				name = "computer_files"
			}
		}
		if name != "computer" && name != "computer_shell" && name != "computer_files" && name != "release_computer" && name != "project_browser" && name != "request_browser_login" && name != "request_chrome_login" && name != "request_computer_help" && name != "computer_transfer" && name != "computer_chrome_grant" && name != "computer_attachment" {
			return "", false, false, nil
		}
		if name != "release_computer" {
			if err := s.EnsureStarted(call, projectID); err != nil {
				return "", true, true, err
			}
		}
		gate := s.lifecycle(projectID)
		gate.RLock()
		defer gate.RUnlock()
		lock := s.lock(projectID)
		lock.Lock()
		defer lock.Unlock()
		if name == "release_computer" {
			err := s.repo.Release(call, projectID, owner)
			if err == nil {
				reserved = false
			}
			s.publishStatus(projectID, false)
			return "Computer released", true, err != nil, err
		}
		if err := s.repo.Reserve(call, projectID, owner); err != nil {
			return "", true, true, err
		}
		if !reserved {
			reserved = true
			s.publishStatus(projectID, false)
		}
		releaseOnce.Do(func() {
			if ctx.Done() == nil {
				return
			}
			go func() {
				ticker := time.NewTicker(time.Minute)
				defer ticker.Stop()
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					s.repo.Release(cleanup, projectID, owner)
					s.publishStatus(projectID, false)
				}()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						s.repo.Renew(ctx, projectID, owner)
					}
				}
			}()
		})
		operation, cancel := context.WithCancel(call)
		defer cancel()
		s.active.Store(projectID+":"+s.taskID, cancel)
		defer s.active.Delete(projectID + ":" + s.taskID)
		current, err := s.repo.Get(operation, projectID)
		if err != nil {
			return "", true, true, err
		}
		if current.Held {
			return "", true, true, persistence.ErrComputerBusy
		}
		call = operation
		conn, err := s.Connection(call, projectID)
		if err != nil {
			return "", true, true, err
		}
		if name == "computer_shell" {
			if err := ctx.Err(); err != nil {
				return "", true, true, err
			}
			cancelExecutionOnce.Do(func() {
				context.AfterFunc(ctx, func() { s.cancelRemoteExecution(conn, executionID) })
			})
		}
		if processTool != "" {
			body, err := json.Marshal(map[string]any{"name": processTool, "input": input, "execution_id": executionID})
			if err != nil {
				return "", true, true, err
			}
			data, err := s.request(call, conn, "POST", "/process-tools", body)
			if err != nil {
				return "", true, true, err
			}
			var result struct {
				Output string `json:"output"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return "", true, true, err
			}
			if explicitProcess {
				// Keep follow-up instructions on the explicit computer target in local mode.
				if prefix, ok := strings.CutSuffix(result.Output, "\nnext: call process with this session_id to read output or send input\n[/process]"); ok {
					result.Output = prefix + "\nnext: call computer_job with this session_id to read output or send input\n[/process]"
				}
			}
			if result.Error != "" {
				return result.Output, true, true, errors.New(result.Error)
			}
			return result.Output, true, false, nil
		}
		if name == "computer_attachment" {
			call = llmcontracts.WithArtifactExecutionID(call, llmcontracts.ArtifactExecutionIDFromContext(ctx))
			out, err := s.attachmentTransfer(call, projectID, input)
			return out, true, err != nil, err
		}
		if name == "computer_transfer" {
			var args struct {
				Direction string `json:"direction"`
				Local     string `json:"local_path"`
				Remote    string `json:"computer_path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", true, true, err
			}
			out, err := s.transfer(call, projectID, args.Direction, args.Local, args.Remote)
			return out, true, err != nil, err
		}
		if name == "computer_chrome_grant" {
			var args struct {
				Action string `json:"action"`
				ID     string `json:"grant_id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", true, true, err
			}
			if args.Action == "list" {
				grants, err := s.ChromeGrants(call, projectID)
				data, _ := json.Marshal(grants)
				return string(data), true, err != nil, err
			}
			if args.Action != "use" {
				return "", true, true, errors.New("choose list or use")
			}
			count, err := s.UseChromeGrant(call, projectID, args.ID)
			data, _ := json.Marshal(map[string]int{"imported": count})
			return string(data), true, err != nil, err
		}
		path := "/action"
		if name == "request_browser_login" || name == "request_chrome_login" || name == "request_computer_help" {
			var args struct {
				URL      string `json:"url"`
				Reason   string `json:"reason"`
				Expected string `json:"expected_text"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", true, true, err
			}
			if len(args.Reason) > 2000 || len(args.Expected) > 500 {
				return "", true, true, errors.New("computer request is too long")
			}
			if name == "request_computer_help" {
				if err := s.cancelJobs(call, projectID, ""); err != nil {
					return "", true, true, err
				}
				if strings.TrimSpace(args.Reason) == "" {
					return "", true, true, errors.New("describe the help needed")
				}
				err := s.changeViewerPermission(projectID, false, func() error { return s.repo.RequestHandoff(call, projectID, name, "", args.Reason, "") })
				return "Computer help requested in Inbox. End this turn; the task resumes after the user responds.", true, err != nil, err
			}
			parsed, err := url.Parse(args.URL)
			if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				return "", true, true, errors.New("provide an HTTP website URL")
			}
			if name == "request_chrome_login" && !s.ChromeImportAvailable() {
				return "", true, true, errors.New("Chrome import requires local macOS; use request_browser_login")
			}
			if strings.TrimSpace(args.Expected) == "" {
				return "", true, true, errors.New("provide specific expected_text proving access to the protected page")
			}
			if err := s.cancelJobs(call, projectID, ""); err != nil {
				return "", true, true, err
			}
			body, _ := json.Marshal(map[string]string{"action": "navigate", "url": args.URL})
			if _, err := s.request(call, conn, "POST", "/browser", body); err != nil {
				return "", true, true, err
			}
			err = s.changeViewerPermission(projectID, false, func() error {
				return s.repo.RequestHandoff(call, projectID, name, args.URL, args.Reason, args.Expected)
			})
			return "Private login requested in Inbox. End this turn. Completing the handoff verifies access and resumes this task.", true, err != nil, err
		}
		if name == "project_browser" {
			var args struct {
				Action string `json:"action"`
			}
			json.Unmarshal(input, &args)
			if !validProjectBrowserAction(args.Action) {
				return "", true, true, errors.New("unsupported browser action")
			}
			path = "/browser"
		}
		if name == "computer_files" {
			path = "/native-files"
		}
		data, err := s.request(call, conn, "POST", path, input)
		if err != nil {
			return "", true, true, err
		}
		if name == "computer" && bytes.Contains(data, []byte(`"content"`)) {
			output, err := agenttools.EncodeImageResult(data)
			return output, true, err != nil, err
		}
		return string(data), true, false, nil
	}}
}

// TaskRuntimeTools binds every operation to the durable task desktop, across turns.
func (s *ProjectComputerService) RuntimeTools(ctx context.Context, projectID, taskID string) *llmcontracts.RuntimeTools {
	if s == nil {
		return nil
	}
	scoped, err := s.ForTask(ctx, projectID, taskID)
	if err != nil {
		return nil
	}
	return scoped.runtimeTools(ctx, projectID, taskID)
}
func (s *ProjectComputerService) ForTask(ctx context.Context, projectID, taskID string) (*ProjectComputerService, error) {
	// Materialize configuration and the desktop record before the first task turn.
	if err := s.projects.Computers().Configure(ctx, projectID, s.provisioner.Provider()); err != nil {
		return nil, err
	}
	repo, err := s.projects.Computers().ForTask(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	return &ProjectComputerService{broadcaster: s.broadcaster, readinessLookups: s.readinessLookups, repo: repo, projects: s.projects, provisioner: s.provisioner, client: s.client, locks: s.locks, active: s.active, taskID: taskID, attachmentDirectory: s.attachmentDirectory, verifyLoginVisual: s.verifyLoginVisual, onFilePublished: s.onFilePublished}, nil
}
func ComputerDesktopID(taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	return "task-" + hex.EncodeToString(sum[:16])
}

func (s *ProjectComputerService) CompleteLogin(ctx context.Context, id string) error {
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !state.Held || state.HandoffKind == "" {
		return errors.New("no computer request awaiting completion")
	}
	if state.HandoffKind != "request_computer_help" && state.HandoffExpectedText != "" {
		conn, err := s.Connection(ctx, id)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]string{"action": "verify_access", "url": state.HandoffURL, "expected_text": state.HandoffExpectedText})
		data, err := s.request(ctx, conn, "POST", "/browser", body)
		if err != nil {
			return errors.New("the requested page is not accessible yet; finish signing in and retry")
		}
		if err := s.verifyLoginResult(ctx, state, data); err != nil {
			return err
		}
	}
	return s.changeViewerPermission(id, false, func() error { return s.repo.FinishHandoff(ctx, id, false) })
}

// CloseTaskDesktop frees processes after deletion while retaining files until project cleanup.
func (s *ProjectComputerService) CloseTaskDesktop(ctx context.Context, projectID, taskID string) error {
	// The task is already deleted. Revoke control even if subsequent cleanup
	// fails or its request context has been cancelled.
	scoped := *s
	scoped.taskID = taskID
	_ = scoped.changeViewerPermission(projectID, false, func() error { return nil })
	if _, err := s.repo.Get(ctx, projectID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	key := projectID + ":" + taskID
	if cancel, ok := s.active.Load(key); ok {
		cancel.(context.CancelFunc)()
	}
	gate := s.lifecycle(projectID)
	gate.RLock()
	defer gate.RUnlock()
	m, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	lock := m.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	conn, err := s.provisioner.Resolve(ctx, projectID)
	if err != nil {
		return nil
	} // A stopped computer has no desktop processes.
	req, err := http.NewRequestWithContext(ctx, "DELETE", conn.Endpoint+"/desktops/"+ComputerDesktopID(taskID)+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	res, err := s.client.Do(req)
	if err != nil {
		return errors.New("could not close task desktop")
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("could not close task desktop")
	}
	return nil
}

func (s *ProjectComputerService) cleanupProjectResources(ctx context.Context, id string) error {
	if local, ok := s.provisioner.(interface {
		RemoveProject(context.Context, string) error
	}); ok {
		return local.RemoveProject(ctx, id)
	}
	return nil
}

func (s *ProjectComputerService) CancelHandoff(ctx context.Context, id string) error {
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	return s.changeViewerPermission(id, false, func() error { return s.repo.FinishHandoff(ctx, id, true) })
}
func (s *ProjectComputerService) SetToolTarget(ctx context.Context, id, target string) error {
	defer s.publishStatus(id, false)
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	return s.repo.SetToolTarget(ctx, id, target)
}

// SetTaskEnabled changes only this task's permission, preserving its desktop data.
func (s *ProjectComputerService) SetTaskEnabled(ctx context.Context, id string, enabled bool) error {
	defer s.publishStatus(id, false)
	if s.taskID == "" {
		return errors.New("task required")
	}
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if enabled && !state.ProjectEnabled {
		return errors.New("enable computer use in project settings first")
	}
	if !enabled && (state.Held || state.Owner != "" && state.LeaseUntil >= time.Now().Unix()) {
		return persistence.ErrComputerBusy
	}
	if enabled {
		return s.repo.SetEnabled(ctx, id, true)
	}
	return s.changeViewerPermission(id, false, func() error { return s.repo.SetEnabled(ctx, id, false) })
}

// SetEnabled changes the project preference shared by all task desktops.
func (s *ProjectComputerService) SetEnabled(ctx context.Context, id string, enabled bool) error {
	defer s.publishStatus(id, true)
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	repo := s.projects.Computers()
	if err := repo.Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	if enabled {
		return repo.SetEnabled(ctx, id, true)
	}
	busy, err := repo.SessionsBusy(ctx, id)
	if err != nil {
		return err
	}
	if busy {
		return persistence.ErrComputerBusy
	}
	state, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if state.Owner != "" && state.LeaseUntil >= time.Now().Unix() {
		return persistence.ErrComputerBusy
	}
	return s.changeViewerPermission(id, true, func() error {
		if _, err := s.provisioner.Resolve(ctx, id); err == nil {
			if err = s.provisioner.Stop(ctx, id); err != nil {
				return err
			}
		}
		return repo.SetEnabled(ctx, id, false)
	})
}

func validateComputerTimeZone(zone string) error {
	if zone == "" || zone == "Local" {
		return errors.New("choose a named time zone, such as America/New_York or UTC")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return errors.New("unknown time zone")
	}
	return nil
}
func (s *ProjectComputerService) SetTimeZone(ctx context.Context, id, zone string) error {
	defer s.publishStatus(id, true)
	if err := validateComputerTimeZone(zone); err != nil {
		return err
	}
	if err := s.repo.Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	return s.repo.SetTimeZone(ctx, id, zone)
}

// A custom/hosted legacy image must not silently restore the old shared-user
// boundary. Test provisioners are deliberately independent of the wire version.
func (s *ProjectComputerService) requireIsolation(data []byte) error {
	switch s.provisioner.(type) {
	case *LocalComputerProvisioner, *HostedComputerProvisioner:
		var health struct {
			Isolation         int `json:"isolation"`
			ViewerConnections int `json:"viewer_connections"`
		}
		if json.Unmarshal(data, &health) != nil || health.Isolation != 1 || health.ViewerConnections != 1 {
			return errors.New("update the project computer image: isolated task desktops and viewer connection tracking are required")
		}
	}
	return nil
}
