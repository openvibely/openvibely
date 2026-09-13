package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestAgentEditModalIgnoresOutOfOrderLifecycleResponsesInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	tc := NewTestContext(t)
	agentRepo := repository.NewAgentRepo(tc.db)
	lifecycleRepo := repository.NewLifecycleRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)
	tc.handler.SetLifecycleRepo(lifecycleRepo)

	ctx := t.Context()
	projects, err := tc.projectRepo.List(ctx)
	if err != nil {
		t.Fatalf("list test projects: %v", err)
	}
	var projectID string
	for _, project := range projects {
		if project.IsDefault {
			projectID = project.ID
			break
		}
	}
	if projectID == "" {
		t.Fatal("default test project not found")
	}
	a := &models.Agent{
		Name:                "Agent A",
		Description:         "agent A description",
		SystemPrompt:        "agent A prompt",
		Key:                 "agent_a_key",
		Scope:               models.AgentScopeGlobal,
		Model:               "inherit",
		SelectableAsPrimary: true,
		Enabled:             true,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true},
		SourceRefs:          []string{"https://example.test/agent-a"},
		CreatedBy:           models.AgentCreatedByUser,
	}
	b := &models.Agent{
		Name:                "Agent B",
		Description:         "agent B description",
		SystemPrompt:        "agent B prompt",
		Key:                 "agent_b_key",
		Scope:               models.AgentScopeProject,
		ProjectID:           projectID,
		Model:               "inherit",
		SelectableAsPrimary: false,
		Enabled:             false,
		PermissionDefaults:  models.AgentPermissionDefaults{WriteSkills: true},
		SourceRefs:          []string{"https://example.test/agent-b"},
		CreatedBy:           models.AgentCreatedByUser,
	}
	protected := &models.Agent{
		Name:                "Protected Agent",
		Description:         "protected agent description",
		SystemPrompt:        "protected agent prompt",
		Key:                 "protected_agent_key",
		Scope:               models.AgentScopeGlobal,
		Model:               "inherit",
		SelectableAsPrimary: true,
		Enabled:             true,
		SystemKind:          "memory_curator",
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
		SourceRefs:          []string{"https://example.test/protected-agent"},
	}
	for _, agent := range []*models.Agent{a, b, protected} {
		if err := agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create %s: %v", agent.Name, err)
		}
	}
	createAgentEditModalTestHook(t, lifecycleRepo, a.ID, "agent_a_hook")
	createAgentEditModalTestHook(t, lifecycleRepo, b.ID, "agent_b_hook")
	createAgentEditModalTestHook(t, lifecycleRepo, protected.ID, "protected_hook")

	htmxJS, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	browserResult := make(chan string, 1)
	var resultOnce sync.Once
	runner := fmt.Sprintf(`<script>
(async function() {
	const agentA = %q;
	const agentB = %q;
	const agentProtected = %q;
	const projectQuery = %q;
	const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
	const fail = message => {
		fetch('/browser-result?status=fail&message=' + encodeURIComponent(String(message))).catch(() => {});
	};
	try {
		for (let i = 0; i < 100 && (!window.htmx || !document.querySelector('[data-agent-id="' + agentA + '"]')); i++) await sleep(20);
		if (!window.htmx) throw new Error('HTMX did not load');
		const originalFetch = window.fetch.bind(window);
		const gate = { active: true, requests: {}, queued: {} };
		const matchAgentLoad = input => {
			const url = new URL(typeof input === 'string' ? input : input.url, window.location.href);
			const match = url.pathname.match(/^\/agents\/([^/]+)\/(json|lifecycle-hooks)$/);
			return match ? decodeURIComponent(match[1]) + '/' + match[2] : '';
		};
		window.fetch = function(input, init) {
			const key = gate.active ? matchAgentLoad(input) : '';
			if (!key) return originalFetch(input, init);
			gate.requests[key] = (gate.requests[key] || 0) + 1;
			return new Promise((resolve, reject) => {
				originalFetch(input, init).then(response => {
					(gate.queued[key] = gate.queued[key] || []).push({ response, resolve });
				}).catch(reject);
			});
		};
		const waitForRequest = async key => {
			for (let i = 0; i < 200 && !(gate.requests[key] > 0); i++) await sleep(10);
			if (!(gate.requests[key] > 0)) throw new Error('timed out waiting for ' + key);
		};
		const release = async key => {
			for (let i = 0; i < 200 && (!gate.queued[key] || gate.queued[key].length === 0); i++) await sleep(10);
			const queued = gate.queued[key] && gate.queued[key].shift();
			if (!queued) throw new Error('timed out waiting to release ' + key);
			queued.resolve(queued.response);
		};
		const clickCard = id => {
			const card = document.querySelector('[data-agent-id="' + id + '"]');
			if (!card) throw new Error('missing card ' + id);
			card.click();
		};
		const waitFor = async (predicate, label) => {
			for (let i = 0; i < 300 && !predicate(); i++) await sleep(20);
			if (!predicate()) throw new Error('timed out waiting for ' + label);
		};

		clickCard(agentA);
		await waitForRequest(agentA + '/json');
		clickCard(agentB);
		await waitForRequest(agentB + '/json');
		await release(agentB + '/json');
		await waitForRequest(agentB + '/lifecycle-hooks');
		await release(agentB + '/lifecycle-hooks');
		await release(agentA + '/json');
		await sleep(100);

		const assertBModalState = () => {
			const key = document.getElementById('advanced_key').value;
			const scope = document.getElementById('advanced_scope').value;
			const enabled = document.getElementById('advanced_enabled').checked;
			const selectable = document.getElementById('advanced_selectable_as_primary').checked;
			const writeSkills = document.getElementById('perm_write_skills').checked;
			const readAgents = document.getElementById('perm_read_agents').checked;
			const sourceRefs = document.getElementById('advanced_source_refs').value;
			const protectedVisible = !document.getElementById('advanced_protected_notice').classList.contains('hidden');
			const hookKeys = Array.from(document.querySelectorAll('[data-hook-field="skill_key"]')).map(input => input.value).join(',');
			if (key !== 'agent_b_key' || scope !== 'project' || enabled || selectable || !writeSkills || readAgents || sourceRefs !== 'https://example.test/agent-b' || protectedVisible || !hookKeys.includes('agent_b_hook')) {
				throw new Error('B modal state was overwritten: ' + JSON.stringify({key, scope, enabled, selectable, writeSkills, readAgents, sourceRefs, protectedVisible, hookKeys}));
			}
		};
		assertBModalState();

		gate.requests = {};
		gate.queued = {};
		clickCard(agentA);
		await waitForRequest(agentA + '/json');
		await release(agentA + '/json');
		await waitForRequest(agentA + '/lifecycle-hooks');
		clickCard(agentB);
		await waitForRequest(agentB + '/json');
		await release(agentB + '/json');
		await waitForRequest(agentB + '/lifecycle-hooks');
		await release(agentB + '/lifecycle-hooks');
		await release(agentA + '/lifecycle-hooks');
		await sleep(100);
		assertBModalState();

		const save = document.getElementById('agent_save_btn');
		if (save.disabled || save.getAttribute('aria-busy') !== 'false') throw new Error('Save remained unavailable after B hydration');
		gate.active = false;
		save.click();
		await waitFor(() => { const modal = document.getElementById('agent_modal'); return modal && !modal.open; }, 'B save response');

		const loadJSON = async path => originalFetch(path + projectQuery).then(response => {
			if (!response.ok) throw new Error(path + ' returned ' + response.status);
			return response.json();
		});
		const savedA = await loadJSON('/agents/' + agentA + '/json');
		const savedB = await loadJSON('/agents/' + agentB + '/json');
		const hooksA = await loadJSON('/agents/' + agentA + '/lifecycle-hooks');
		const hooksB = await loadJSON('/agents/' + agentB + '/lifecycle-hooks');
		if (savedA.key !== 'agent_a_key' || savedA.enabled !== true || !savedA.permission_defaults.read_agents || savedA.source_refs[0] !== 'https://example.test/agent-a' || savedB.key !== 'agent_b_key' || savedB.scope !== 'project' || savedB.enabled !== false || savedB.selectable_as_primary !== false || !savedB.permission_defaults.write_skills || savedB.source_refs[0] !== 'https://example.test/agent-b' || hooksA[0].skill_key !== 'agent_a_hook' || hooksB[0].skill_key !== 'agent_b_hook') {
			throw new Error('persisted A/B state was mixed: ' + JSON.stringify({savedA, savedB, hooksA, hooksB}));
		}
		gate.active = true;
		gate.requests = {};
		gate.queued = {};
		clickCard(agentProtected);
		await waitForRequest(agentProtected + '/json');
		clickCard(agentB);
		await waitForRequest(agentB + '/json');
		await release(agentB + '/json');
		await waitForRequest(agentB + '/lifecycle-hooks');
		await release(agentB + '/lifecycle-hooks');
		await release(agentProtected + '/json');
		await sleep(100);
		const regularHookKeys = Array.from(document.querySelectorAll('[data-hook-field="skill_key"]')).map(input => input.value);
		if (document.getElementById('advanced_key').value !== 'agent_b_key' || !document.getElementById('advanced_enabled') || !document.getElementById('advanced_scope') || document.getElementById('advanced_key').disabled || document.getElementById('advanced_scope').disabled || !document.getElementById('advanced_protected_notice').classList.contains('hidden') || regularHookKeys.length !== 1 || regularHookKeys[0] !== 'agent_b_hook') {
			throw new Error('protected-to-regular switch left protected state active: ' + JSON.stringify({key: document.getElementById('advanced_key').value, keyDisabled: document.getElementById('advanced_key').disabled, scopeDisabled: document.getElementById('advanced_scope').disabled, protectedVisible: !document.getElementById('advanced_protected_notice').classList.contains('hidden'), hookKeys: regularHookKeys}));
		}
		gate.requests = {};
		gate.queued = {};
		clickCard(agentB);
		await waitForRequest(agentB + '/json');
		clickCard(agentProtected);
		await waitForRequest(agentProtected + '/json');
		await release(agentProtected + '/json');
		await waitForRequest(agentProtected + '/lifecycle-hooks');
		await release(agentProtected + '/lifecycle-hooks');
		await release(agentB + '/json');
		await sleep(100);
		if (document.getElementById('advanced_key').value !== 'protected_agent_key' || !document.getElementById('advanced_key').disabled || !document.getElementById('advanced_scope').disabled || document.getElementById('advanced_protected_notice').classList.contains('hidden') || !Array.from(document.querySelectorAll('[data-hook-field="skill_key"]')).some(input => input.value === 'protected_hook')) {
			throw new Error('regular-to-protected switch lost protected state: ' + JSON.stringify({key: document.getElementById('advanced_key').value, keyDisabled: document.getElementById('advanced_key').disabled, scopeDisabled: document.getElementById('advanced_scope').disabled, protectedVisible: !document.getElementById('advanced_protected_notice').classList.contains('hidden'), hookKeys: Array.from(document.querySelectorAll('[data-hook-field="skill_key"]')).map(input => input.value)}));
		}

		gate.requests = {};
		gate.queued = {};
		clickCard(agentProtected);
		await waitForRequest(agentProtected + '/json');
		clickCard(agentProtected);
		await waitForRequest(agentProtected + '/json');
		await release(agentProtected + '/json');
		await release(agentProtected + '/json');
		await waitForRequest(agentProtected + '/lifecycle-hooks');
		await release(agentProtected + '/lifecycle-hooks');
		if (document.getElementById('advanced_key').value !== 'protected_agent_key' || document.getElementById('advanced_protected_notice').classList.contains('hidden')) {
			throw new Error('reopening same protected agent lost current hydration');
		}
		await originalFetch('/browser-result?status=pass&message=out-of-order JSON and hook responses preserved B and persisted A/B state');
	} catch (err) {
		fail(err && err.stack ? err.stack : err);
	}
})();
	</script>`, a.ID, b.ID, protected.ID, "?project_id="+url.QueryEscape(projectID))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
			return
		case "/browser-result":
			resultOnce.Do(func() {
				browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			})
			w.WriteHeader(http.StatusNoContent)
			return
		case "/agents":
			if r.Method == http.MethodGet {
				rec := httptest.NewRecorder()
				tc.echo.ServeHTTP(rec, r)
				if rec.Code != http.StatusOK {
					http.Error(w, rec.Body.String(), rec.Code)
					return
				}
				page := strings.Replace(rec.Body.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
				page = strings.Replace(page, "</body>", runner+"</body>", 1)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(page))
				return
			}
		}
		tc.echo.ServeHTTP(w, r)
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "agent-modal-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"), server.URL+"/agents?project_id="+url.QueryEscape(projectID))
	cmd.Stderr = stderrFile
	if err := startHandlerBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()

	var outcome string
	select {
	case outcome = <-browserResult:
		stopHandlerBrowserProcess(cmd)
		stopped = true
	case <-time.After(60 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("agent edit modal browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}

	storedA, err := agentRepo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("reload A after browser save: %v", err)
	}
	storedB, err := agentRepo.GetByID(ctx, b.ID)
	if err != nil {
		t.Fatalf("reload B after browser save: %v", err)
	}
	if storedA == nil || storedB == nil || storedA.Key != "agent_a_key" || !storedA.Enabled || !storedA.PermissionDefaults.ReadAgents || storedB.Key != "agent_b_key" || storedB.Scope != models.AgentScopeProject || storedB.ProjectID != projectID || storedB.Enabled || storedB.SelectableAsPrimary || !storedB.PermissionDefaults.WriteSkills {
		t.Fatalf("repository state was mixed after browser save: A=%+v B=%+v", storedA, storedB)
	}
	hooksA, err := lifecycleRepo.HooksByAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("reload A hooks: %v", err)
	}
	hooksB, err := lifecycleRepo.HooksByAgent(ctx, b.ID)
	if err != nil {
		t.Fatalf("reload B hooks: %v", err)
	}
	if len(hooksA) != 1 || hooksA[0].SkillKey != "agent_a_hook" || len(hooksB) != 1 || hooksB[0].SkillKey != "agent_b_hook" {
		t.Fatalf("repository hooks were mixed after browser save: A=%+v B=%+v", hooksA, hooksB)
	}
}

func createAgentEditModalTestHook(t *testing.T, repo *repository.LifecycleRepo, agentID, skillKey string) {
	t.Helper()
	if err := repo.CreateHook(t.Context(), &models.AgentLifecycleHook{
		AgentID:        agentID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       skillKey,
		OutputContract: models.OutputContractContextBlock,
		Blocking:       true,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("create %s hook: %v", skillKey, err)
	}
}
