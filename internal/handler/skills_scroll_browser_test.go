package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func TestSkillsDeleteBrowserPreservesFilteredScrollAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	skills := make([]pages.SkillCard, 0, 64)
	for i := 0; i < 64; i++ {
		group := "drop"
		if i%2 == 0 {
			group = "keep"
		}
		skills = append(skills, pages.SkillCard{
			Handle:      fmt.Sprintf("skill_%02d", i),
			Name:        fmt.Sprintf("%s skill %02d", group, i),
			Description: group + " searchable skill",
			Scope:       "global",
			Source:      "global",
			Content:     "body",
			Enabled:     true,
		})
	}
	replacementSkills := make([]pages.SkillCard, 0, len(skills)-1)
	for _, skill := range skills {
		if skill.Handle != "skill_30" {
			replacementSkills = append(replacementSkills, skill)
		}
	}

	initial := renderSkillsContentForBrowserTest(t, skills)
	replacement := renderSkillsContentForBrowserTest(t, replacementSkills)
	replacementJSON, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("marshal replacement: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Skills delete browser", nil, "project-1").Render(context.Background(), &base); err != nil {
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
  #skills-container { padding: 16px; }
  .grid { display: block; }
  .card { display: block; min-height: 96px; margin: 0 0 14px 0; border: 1px solid #ccc; box-sizing: border-box; }
  .card-body { padding: 12px; }
  .dropdown-content { display: none; }
  .dropdown:focus-within .dropdown-content { display: block; }
  .hidden { display: none !important; }
  dialog:not([open]) { display: none; }
</style></head>`, 1)
	page = strings.Replace(page, "</main>", initial+"</main>", 1)
	runner := `<script>
(function() {
  window.htmx = {
    process: function() {},
    ajax: function(method, url, options) {
      window.lastSkillMutationURL = url;
      window.scrollTo(0, document.body.scrollHeight);
      var target = document.querySelector(options.target);
      document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap', {detail: {target: target}}));
      target.outerHTML = replacementHTML;
      var next = document.getElementById('skills-container');
      document.body.dispatchEvent(new CustomEvent('htmx:afterSwap', {detail: {target: next}}));
      document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail: {target: next}}));
      return Promise.resolve();
    }
  };
  var replacementHTML = ` + string(replacementJSON) + `;
})();
</script>
<script>
(function() {
  function finish(status, message) {
    var result = document.createElement('div');
    result.id = 'browser-result';
    result.setAttribute('data-status', status);
    result.textContent = message || status;
    document.body.appendChild(result);
  }
  function run() {
    try {
      var input = document.querySelector('input[data-card-search="skills"]');
      input.value = 'keep';
      window.refreshCardSearches(document.getElementById('skills-container'));
      window._skillScrollOps = [];
      var originalScrollBy = window.scrollBy.bind(window);
      var originalScrollTo = window.scrollTo.bind(window);
      window.scrollBy = function(x, y) { window._skillScrollOps.push('by:' + x + ',' + y); return originalScrollBy(x, y); };
      window.scrollTo = function(x, y) { window._skillScrollOps.push('to:' + x + ',' + y); return originalScrollTo(x, y); };
      var deleted = document.querySelector('[data-skill-handle="skill_30"]');
      var survivor = document.querySelector('[data-skill-handle="skill_32"]');
      if (!deleted || !survivor) return finish('fail', 'missing initial cards');
      deleted.scrollIntoView();
      window.scrollBy(0, -120);
      var beforeTop = survivor.getBoundingClientRect().top;
      window.deleteSkillHandle = 'skill_30';
      window.deleteSkillScope = 'global';
      var modal = document.getElementById('delete_skill_confirm_modal');
      if (modal && modal.showModal) {
        modal.showModal();
        var deleteButton = modal.querySelector('.btn-error');
        if (deleteButton) deleteButton.focus();
      }
      window.confirmDeleteSkill();
      setTimeout(function() {
        try {
          var nextSurvivor = document.querySelector('[data-skill-handle="skill_32"]');
          var removed = !document.querySelector('[data-skill-handle="skill_30"]');
          var hiddenDrop = document.querySelector('[data-skill-handle="skill_31"]');
          var afterTop = nextSurvivor ? nextSurvivor.getBoundingClientRect().top : 9999;
          var delta = Math.abs(afterTop - beforeTop);
          var filtered = hiddenDrop && hiddenDrop.getClientRects().length === 0;
          var focusedDropdown = !!(document.activeElement && document.activeElement.closest && document.activeElement.closest('.dropdown'));
          var openDropdowns = Array.prototype.slice.call(document.querySelectorAll('.dropdown-content')).filter(function(menu) {
            return menu.getClientRects().length > 0;
          });
          var focusedSurvivor = nextSurvivor && document.activeElement === nextSurvivor;
          var mutationURL = new URL(window.lastSkillMutationURL || '', window.location.href);
          var statePreserved = mutationURL.searchParams.get('target_scope') === 'global' && mutationURL.searchParams.get('scope') === null && mutationURL.searchParams.get('project_id') === 'project-1' && mutationURL.searchParams.get('search') === 'keep' && mutationURL.searchParams.get('enabled') === 'true' && mutationURL.searchParams.get('always_use') === 'false' && mutationURL.searchParams.get('archived') === 'false' && mutationURL.searchParams.get('source') === 'global' && mutationURL.searchParams.get('sort') === 'source';
          if (removed && filtered && delta <= 6 && !focusedDropdown && openDropdowns.length === 0 && focusedSurvivor && statePreserved) {
            finish('pass', 'anchor and query state preserved delta=' + delta.toFixed(2));
          } else {
            var state = window.openVibelySkillsViewport || {};
            finish('fail', 'removed=' + removed + ' filtered=' + filtered + ' delta=' + delta.toFixed(2) + ' focusedDropdown=' + focusedDropdown + ' openDropdowns=' + openDropdowns.length + ' focusedSurvivor=' + focusedSurvivor + ' statePreserved=' + statePreserved + ' mutationURL=' + (window.lastSkillMutationURL || '') + ' active=' + (document.activeElement && document.activeElement.tagName) + ' scrollY=' + window.scrollY + ' installed=' + !!state.installed + ' prepared=' + !!state.preparedSwap + ' swap=' + !!state.swap + ' ops=' + (window._skillScrollOps || []).join('|'));
          }
        } catch (err) {
          finish('fail', err && err.stack ? err.stack : String(err));
        }
      }, 0);
    } catch (err) {
      finish('fail', err && err.stack ? err.stack : String(err));
    }
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', run);
  else run();
})();
</script>`
	page = strings.Replace(page, "</body>", runner+"</body>", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-extensions",
		"--no-default-browser-check",
		"--no-first-run",
		"--virtual-time-budget=3000",
		"--dump-dom",
		srv.URL,
	)
	out, err := cmd.CombinedOutput()
	dom := string(out)
	passed := strings.Contains(dom, `id="browser-result" data-status="pass"`)
	if ctx.Err() != nil {
		if passed {
			return
		}
		t.Fatalf("chrome timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("chrome failed: %v\n%s", err, out)
	}
	if !passed {
		idx := strings.Index(dom, `data-status=`)
		if idx >= 0 {
			end := idx + 500
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser regression did not report a result; DOM length=%d", len(dom))
	}
}

func renderSkillsContentForBrowserTest(t *testing.T, skills []pages.SkillCard) string {
	t.Helper()
	var buf bytes.Buffer
	state := pages.CardListState{
		ProjectID: "project-1",
		Search:    "keep",
		Filters: map[string]string{
			"enabled":    "true",
			"always_use": "false",
			"archived":   "false",
			"source":     "global",
		},
		Sort: "source",
	}
	if err := pages.SkillsContentForProjectPageWithState(skills, true, "project-1", false, state).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render skills content: %v", err)
	}
	return buf.String()
}

func findChromeForBrowserTest(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("CHROME_BIN"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}
	candidates := []string{"google-chrome", "chromium", "chromium-browser", "chrome"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}, candidates...)
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			candidates = append(candidates, filepath.Join(local, `Google\Chrome\Application\chrome.exe`))
		}
		if programFiles := os.Getenv("PROGRAMFILES"); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, string(os.PathSeparator)) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}
