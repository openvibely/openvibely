package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func TestModelsSortBrowserPreservesViewportFocusStateAndSharedGeometry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Models sort browser").Build()
	for i := 0; i < 40; i++ {
		config := &models.LLMConfig{
			Name:     fmt.Sprintf("Window Model %02d", i),
			Provider: models.ProviderTest,
			Model:    fmt.Sprintf("window-test-%02d", i),
		}
		if err := tc.llmConfigRepo.Create(t.Context(), config); err != nil {
			t.Fatalf("create browser model %d: %v", i, err)
		}
	}
	query := "project_id=" + project.ID + "&search=Window+Model&kind=direct"
	initialRec := serveCardPageRequest(t, tc.echo, "/models?"+query+"&sort=name_asc")
	if initialRec.Code != http.StatusOK {
		t.Fatalf("render initial Models window: %d %s", initialRec.Code, initialRec.Body.String())
	}
	initial := initialRec.Body.String()
	if count := strings.Count(initial, `data-model-provider=`); count != 20 {
		t.Fatalf("expected initial Models request to render the first 20 cards, got %d", count)
	}
	var base bytes.Buffer
	if err := layout.Base("Models sort browser", nil, project.ID).Render(context.Background(), &base); err != nil {
		t.Fatalf("render base: %v", err)
	}
	var local []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		local = append(local, line)
	}
	page := strings.Replace(strings.Join(local, "\n"), "</head>", `<style>
		html, body { margin: 0; padding: 0; }
		body { font-family: sans-serif; }
		#models-container { padding: 16px; }
		.grid { display: block; }
		.card { display: block; min-height: 104px; margin-bottom: 12px; border: 1px solid #ccc; box-sizing: border-box; }
		.card-body { padding: 12px; }
		.hidden { display: none !important; }
		.card-sort-control { width: 11rem !important; max-width: 100%; }
		[data-geometry-toolbar] { display: inline-block; margin-right: 8px; }
	</style></head>`, 1)
	page = strings.Replace(page, "</main>", initial+"</main>", 1)
	page = strings.Replace(page, "</body>", geometryToolbarFixture(t)+"</body>", 1)
	publicPath := "/models?" + query + "&sort=name_asc"
	publicPathJSON, err := json.Marshal(publicPath)
	if err != nil {
		t.Fatalf("marshal public path: %v", err)
	}
	projectIDJSON, err := json.Marshal(project.ID)
	if err != nil {
		t.Fatalf("marshal project ID: %v", err)
	}
	runner := `<script>
	(function() {
		var publicPath = ` + string(publicPathJSON) + `;
		var projectID = ` + string(projectIDJSON) + `;
		function finish(status, message) {
			var result = document.createElement('div');
			result.id = 'browser-result';
			result.setAttribute('data-status', status);
			result.textContent = message || status;
			document.body.appendChild(result);
		}
		function run() {
			try {
				var initializedRoot = document.getElementById('models-container');
				if (!initializedRoot || !initializedRoot._openVibelyCardPaginationState) {
					setTimeout(run, 25);
					return;
				}
				var root = initializedRoot;
				var initialCards = root.querySelectorAll('[data-search-card]');
				if (initialCards.length < 40) {
					window._modelsLoadAttempts = (window._modelsLoadAttempts || 0) + 1;
					if (window._modelsLoadAttempts > 80) {
						finish('fail', 'timed out loading Models window; cards=' + initialCards.length);
						return;
					}
					window.scrollTo(0, document.documentElement.scrollHeight);
					window.dispatchEvent(new Event('scroll'));
					setTimeout(run, 25);
					return;
				}
				history.replaceState({}, '', publicPath);
				window.openVibelyNavigate = function(requestPath, historyPath) {
					window._modelsSortRequestPath = requestPath;
					window._modelsSortHistoryPath = historyPath || requestPath;
					return fetch(requestPath, {headers: {'HX-Request': 'true'}}).then(function(response) {
						if (!response.ok) throw new Error('Models sort request failed: ' + response.status);
						return response.text();
					}).then(function(fragment) {
						history.replaceState({}, '', historyPath || requestPath);
						var main = document.getElementById('main-content');
						document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap', {detail: {target: main}}));
						main.innerHTML = fragment;
						document.body.dispatchEvent(new CustomEvent('htmx:afterSwap', {detail: {target: main, elt: main}}));
						document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail: {target: main, elt: main}}));
					});
				};
				initialCards = root.querySelectorAll('[data-search-card]');
				var anchor = initialCards[30];
				if (!anchor) {
					finish('fail', 'initial card count=' + initialCards.length + ' modelIDs=' + root.querySelectorAll('[data-model-id]').length + ' list=' + !!root.querySelector('#models-card-list'));
					return;
				}
				anchor.scrollIntoView();
				window.scrollBy(0, -140);
				var before = window.scrollY;
				var sort = root.querySelector('[data-card-sort]');
				sort.focus({preventScroll: true});
				sort.value = 'name_desc';
				sort.dispatchEvent(new Event('change', {bubbles: true}));
				setTimeout(function() {
					try {
						var nextRoot = document.getElementById('models-container');
						var nextSort = nextRoot && nextRoot.querySelector('[data-card-sort]');
						var after = window.scrollY;
						var requestURL = new URL(window._modelsSortRequestPath || '', window.location.href);
						var historyURL = new URL(window._modelsSortHistoryPath || '', window.location.href);
						var requestOK = requestURL.searchParams.get('card_window') === '1' && requestURL.searchParams.get('page_size') === '40' && requestURL.searchParams.get('page') === '0' && requestURL.searchParams.get('sort') === 'name_desc';
						var stateOK = historyURL.searchParams.get('project_id') === projectID && historyURL.searchParams.get('search') === 'Window Model' && historyURL.searchParams.get('kind') === 'direct' && historyURL.searchParams.get('sort') === 'name_desc' && !historyURL.searchParams.has('card_window') && !historyURL.searchParams.has('page_size') && !historyURL.searchParams.has('page') && !historyURL.searchParams.has('offset');
						var windowOK = nextRoot && nextRoot.querySelectorAll('[data-search-card]').length === 40;
						var controls = Array.prototype.slice.call(document.querySelectorAll('.card-sort-control'));
						var widths = controls.map(function(control) { return control.getBoundingClientRect().width; });
						var geometryOK = widths.length === 4 && widths.every(function(width) { return Math.abs(width - widths[0]) < 0.5 && width <= window.innerWidth; });
						var labelsFit = controls.every(function(control) { return control.scrollWidth <= control.clientWidth + 1; });
						var scrollOK = Math.abs(after - before) <= 2;
						var focusOK = document.activeElement === nextSort;
						var selectedOK = nextSort && nextSort.value === 'name_desc';
						if (scrollOK && focusOK && selectedOK && stateOK && requestOK && windowOK && geometryOK && labelsFit) finish('pass', 'scroll=' + before + '/' + after + ' width=' + widths.join(','));
						else finish('fail', 'scrollOK=' + scrollOK + ' focusOK=' + focusOK + ' selectedOK=' + selectedOK + ' stateOK=' + stateOK + ' requestOK=' + requestOK + ' windowOK=' + windowOK + ' geometryOK=' + geometryOK + ' labelsFit=' + labelsFit + ' before=' + before + ' after=' + after + ' count=' + (nextRoot ? nextRoot.querySelectorAll('[data-search-card]').length : -1) + ' request=' + (window._modelsSortRequestPath || '') + ' history=' + (window._modelsSortHistoryPath || ''));
					} catch (error) { finish('fail', error && error.stack ? error.stack : String(error)); }
				}, 150);
			} catch (error) { finish('fail', error && error.stack ? error.stack : String(error)); }
		}
		setTimeout(run, 50);
	})();
	</script>`
	page = strings.Replace(page, "</body>", runner+"</body>", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			tc.echo.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check", "--no-first-run",
		"--virtual-time-budget=3000", "--dump-dom", srv.URL,
	)
	out, runErr := cmd.CombinedOutput()
	dom := string(out)
	if ctx.Err() != nil && !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		t.Fatalf("chrome timed out: %v\n%s", ctx.Err(), out)
	}
	if runErr != nil {
		t.Fatalf("chrome failed: %v\n%s", runErr, out)
	}
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 900
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser regression did not report a result; DOM length=%d", len(dom))
	}
}

func TestChannelsNameSortBrowserOrdersMixedCards(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	view := pages.ChannelsSettingsView{
		CurrentProjectID: "project-channels-browser",
		HasGitHubChannel: true,
		HasSlackChannel:  true,
		HasXChannel:      true,
		HasEmailChannel:  true,
		Sort:             "name_desc",
		WebhooksHasMore:  true,
		ChannelTargets: []models.ChannelTarget{
			{ID: "outbound-target", Name: "Team destination", Platform: "slack", TargetID: "channel-1"},
		},
	}
	for i := 0; i < 20; i++ {
		webhook := models.WebhookEndpoint{
			ID: fmt.Sprintf("webhook-%02d", i), Name: fmt.Sprintf("Webhook %02d", 19-i), Enabled: true,
		}
		if i == 0 {
			webhook.ID = "alpha-github-hook"
			webhook.Name = "GitHub"
		}
		view.Webhooks = append(view.Webhooks, webhook)
	}
	nextView := view
	nextView.Webhooks = []models.WebhookEndpoint{
		{ID: "zulu-github-hook", Name: "GitHub", Enabled: true},
		{ID: "mix-hook", Name: "mix webhook", Enabled: true},
	}
	nextView.WebhooksHasMore = false
	var nextContent bytes.Buffer
	if err := pages.SettingsContent(nextView).Render(context.Background(), &nextContent); err != nil {
		t.Fatalf("render next Channels page: %v", err)
	}
	var content bytes.Buffer
	if err := pages.SettingsContent(view).Render(context.Background(), &content); err != nil {
		t.Fatalf("render Channels content: %v", err)
	}
	var base bytes.Buffer
	if err := layout.Base("Channels sort browser", nil, view.CurrentProjectID).Render(context.Background(), &base); err != nil {
		t.Fatalf("render base: %v", err)
	}
	var local []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		local = append(local, line)
	}
	page := strings.Replace(strings.Join(local, "\n"), "</main>", content.String()+"</main>", 1)
	runner := `<script>
	(function() {
		function finish(status, message) {
			var result = document.createElement('div');
			result.id = 'browser-result';
			result.setAttribute('data-status', status);
			result.textContent = message;
			document.body.appendChild(result);
		}
		function run() {
			var root = document.getElementById('channels-container');
			if (!root || !root._openVibelyCardPaginationState) {
				setTimeout(run, 25);
				return;
			}
			var webhookCount = root.querySelectorAll('[data-webhook-id]').length;
			if (webhookCount < 21) {
				window._channelsLoadAttempts = (window._channelsLoadAttempts || 0) + 1;
				if (window._channelsLoadAttempts > 80) {
					finish('fail', 'timed out loading Channels window; webhooks=' + webhookCount);
					return;
				}
				window.scrollTo(0, document.documentElement.scrollHeight);
				window.dispatchEvent(new Event('scroll'));
				setTimeout(run, 25);
				return;
			}
			var names = Array.prototype.map.call(
				document.querySelectorAll('#channel-card-list [data-card-sort-name]'),
				function(card) { return card.getAttribute('data-card-sort-name'); }
			);
			var sorted = names.every(function(name, index) {
				if (!index) return true;
				return names[index - 1].toLocaleLowerCase() >= name.toLocaleLowerCase();
			});
			var fixedGitHub = root.querySelector('[data-channel-type="github"]');
			var initialGitHubWebhook = root.querySelector('[data-webhook-id="alpha-github-hook"]');
			var appendedGitHubWebhook = root.querySelector('[data-webhook-id="zulu-github-hook"]');
			var equalNameOrder = !!(appendedGitHubWebhook && initialGitHubWebhook && fixedGitHub &&
				(appendedGitHubWebhook.compareDocumentPosition(initialGitHubWebhook) & Node.DOCUMENT_POSITION_FOLLOWING) &&
				(initialGitHubWebhook.compareDocumentPosition(fixedGitHub) & Node.DOCUMENT_POSITION_FOLLOWING));
			var complete = names.length === 27 && names.indexOf('mix webhook') !== -1 && names.indexOf('X (formerly Twitter)') !== -1 && names.indexOf('Outbound Message Targets') !== -1;
			finish(sorted && equalNameOrder && complete ? 'pass' : 'fail', names.join('|') + '; equalNameOrder=' + equalNameOrder);
		}
		setTimeout(run, 50);
	})();
	</script>`
	page = strings.Replace(page, "</body>", runner+"</body>", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/channels" {
			w.Header().Set(cardPageHasMoreHeader, "false")
			_, _ = w.Write(nextContent.Bytes())
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check", "--no-first-run",
		"--virtual-time-budget=3000", "--dump-dom", srv.URL,
	)
	out, runErr := cmd.CombinedOutput()
	dom := string(out)
	if runErr != nil {
		t.Fatalf("chrome failed: %v\n%s", runErr, out)
	}
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := min(idx+700, len(dom))
			t.Fatalf("browser regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser regression did not report a result; DOM length=%d", len(dom))
	}
}

func geometryToolbarFixture(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	for _, config := range []pages.CardListToolbarConfig{
		{PageKey: "channels-geometry", Sort: "name_asc", SortOptions: []pages.CardListOption{{Value: "name_asc", Label: "Name A–Z"}, {Value: "name_desc", Label: "Name Z–A"}}},
		{PageKey: "automations-geometry", Sort: "updated_asc", SortOptions: []pages.CardListOption{{Value: "name_asc", Label: "Name A–Z"}, {Value: "updated_asc", Label: "Least recently updated"}}},
		{PageKey: "personality-geometry", Sort: "name_desc", SortOptions: []pages.CardListOption{{Value: "name_asc", Label: "Name A–Z"}, {Value: "name_desc", Label: "Name Z–A"}}},
	} {
		buf.WriteString(`<div data-geometry-toolbar>`)
		if err := pages.CardListToolbar(config).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render geometry toolbar: %v", err)
		}
		buf.WriteString(`</div>`)
	}
	return buf.String()
}
