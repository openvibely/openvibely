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
			Scope:       "project",
			Source:      "project",
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

	page := `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<style>
  html, body { margin: 0; padding: 0; }
  body { font-family: sans-serif; }
  #skills-container { padding: 16px; }
  .grid { display: block; }
  .card { display: block; min-height: 96px; margin: 0 0 14px 0; border: 1px solid #ccc; box-sizing: border-box; }
  .card-body { padding: 12px; }
  .hidden { display: none !important; }
  dialog:not([open]) { display: none; }
</style>
</head>
<body>
` + initial + `
<script>
(function() {
  function storageKey(pageKey) { return 'ov_card_search_' + pageKey; }
  window.refreshCardSearches = function(root) {
    root = root || document;
    var inputs = root.querySelectorAll('input[data-card-search]');
    for (var i = 0; i < inputs.length; i++) {
      var input = inputs[i];
      var pageKey = input.getAttribute('data-card-search') || '';
      var saved = pageKey ? sessionStorage.getItem(storageKey(pageKey)) || '' : '';
      if (!input.value && saved) input.value = saved;
      if (pageKey) sessionStorage.setItem(storageKey(pageKey), input.value || '');
      var container = input.closest('[data-search-container]');
      if (!container) continue;
      var term = (input.value || '').toLowerCase().trim();
      var cards = container.querySelectorAll('[data-search-card]');
      for (var j = 0; j < cards.length; j++) {
        var text = (cards[j].getAttribute('data-search-text') || '').toLowerCase();
        cards[j].style.display = (!term || text.indexOf(term) !== -1) ? '' : 'none';
      }
    }
  };
  window.htmx = {
    process: function() {},
    ajax: function(method, url, options) {
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
      window.deleteSkillScope = 'project';
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
          if (removed && filtered && delta <= 6) {
            finish('pass', 'anchor preserved delta=' + delta.toFixed(2));
          } else {
            var state = window.openVibelySkillsViewport || {};
            finish('fail', 'removed=' + removed + ' filtered=' + filtered + ' delta=' + delta.toFixed(2) + ' scrollY=' + window.scrollY + ' installed=' + !!state.installed + ' prepared=' + !!state.preparedSwap + ' swap=' + !!state.swap + ' ops=' + (window._skillScrollOps || []).join('|'));
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
</script>
</body>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--virtual-time-budget=3000",
		"--dump-dom",
		srv.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("chrome timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("chrome failed: %v\n%s", err, out)
	}
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
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
	if err := pages.SkillsContent(skills, true).Render(context.Background(), &buf); err != nil {
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
