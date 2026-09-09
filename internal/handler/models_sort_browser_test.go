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

	configs := make([]models.LLMConfig, 40)
	for i := range configs {
		configs[i] = models.LLMConfig{
			ID:       fmt.Sprintf("model-%02d", i),
			Name:     fmt.Sprintf("Model %02d", i),
			Provider: models.ProviderTest,
			Model:    fmt.Sprintf("test-%02d", i),
		}
	}
	initial := renderModelsSortBrowserContent(t, configs, "name_asc")
	reversed := append([]models.LLMConfig(nil), configs...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	replacement := renderModelsSortBrowserContent(t, reversed, "name_desc")
	replacementJSON, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("marshal replacement: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Models sort browser", nil, "project-1").Render(context.Background(), &base); err != nil {
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
	page = strings.Replace(page, "</main>", initial+geometryToolbarFixture(t)+"</main>", 1)
	runner := `<script>
	(function() {
		var replacementHTML = ` + string(replacementJSON) + `;
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
				history.replaceState({}, '', '/models?project_id=project-1&search=Model&kind=direct&page=3&offset=60');
			window.openVibelyNavigate = function(path) {
				window._modelsSortPath = path;
				window._modelsActiveBeforeSwap = document.activeElement && (document.activeElement.id || document.activeElement.tagName);					history.replaceState({}, '', path);
					var oldRoot = document.getElementById('models-container');
					document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap', {detail: {target: oldRoot}}));
					oldRoot.outerHTML = replacementHTML;
					var next = document.getElementById('models-container');
					document.body.dispatchEvent(new CustomEvent('htmx:afterSwap', {detail: {target: next, elt: next}}));
					document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail: {target: next, elt: next}}));
				};
				var root = document.getElementById('models-container');
				var anchor = root.querySelector('[data-model-id="model-12"]');
				anchor.scrollIntoView();
				window.scrollBy(0, -140);
				var before = window.scrollY;
				var sort = root.querySelector('[data-card-sort]');
				sort.focus({preventScroll: true});
				sort.value = 'name_desc';
				sort.dispatchEvent(new Event('change', {bubbles: true}));
				setTimeout(function() {
					try {
						var nextSort = document.querySelector('#models-container [data-card-sort]');
						var after = window.scrollY;
						var parsed = new URL(window._modelsSortPath || '', window.location.href);
						var stateOK = parsed.searchParams.get('project_id') === 'project-1' && parsed.searchParams.get('search') === 'Model' && parsed.searchParams.get('kind') === 'direct' && parsed.searchParams.get('sort') === 'name_desc' && !parsed.searchParams.has('page') && !parsed.searchParams.has('offset');
						var controls = Array.prototype.slice.call(document.querySelectorAll('.card-sort-control'));
						var widths = controls.map(function(control) { return control.getBoundingClientRect().width; });
						var geometryOK = widths.length === 4 && widths.every(function(width) { return Math.abs(width - widths[0]) < 0.5 && width <= window.innerWidth; });
						var labelsFit = controls.every(function(control) { return control.scrollWidth <= control.clientWidth + 1; });
						var scrollOK = Math.abs(after - before) <= 2;
				var focusOK = document.activeElement === nextSort;
				var selectedOK = nextSort && nextSort.value === 'name_desc';						if (scrollOK && focusOK && selectedOK && stateOK && geometryOK && labelsFit) finish('pass', 'scroll=' + before + '/' + after + ' width=' + widths.join(','));
						else finish('fail', 'scrollOK=' + scrollOK + ' focusOK=' + focusOK + ' selectedOK=' + selectedOK + ' stateOK=' + stateOK + ' geometryOK=' + geometryOK + ' labelsFit=' + labelsFit + ' activeBefore=' + (window._modelsActiveBeforeSwap || '') + ' active=' + (document.activeElement && (document.activeElement.id || document.activeElement.tagName)) + ' before=' + before + ' after=' + after + ' widths=' + widths.join(',') + ' path=' + (window._modelsSortPath || ''));
					} catch (error) { finish('fail', error && error.stack ? error.stack : String(error)); }
				}, 50);
			} catch (error) { finish('fail', error && error.stack ? error.stack : String(error)); }
		}
		setTimeout(run, 50);
	})();
	</script>`
	page = strings.Replace(page, "</body>", runner+"</body>", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser regression did not report a result; DOM length=%d", len(dom))
	}
}

func renderModelsSortBrowserContent(t *testing.T, configs []models.LLMConfig, sortValue string) string {
	t.Helper()
	var buf bytes.Buffer
	state := pages.CardListState{ProjectID: "project-1", Search: "Model", Sort: sortValue, Filters: map[string]string{"kind": "direct"}}
	if err := pages.ModelsContentPageWithPaginationAndState(configs, configs, map[string]int{}, false, false, state).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	return buf.String()
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
