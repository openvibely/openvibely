package pages

import (
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/transcript"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_ByteWindowScrollAndCache(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	latest := strings.Repeat("Latest paragraph with **formatting**.\n\n", 120) + "LATEST_END"
	earlier := "EARLIER_UNIQUE\n\n```go\nfmt.Println(1)\n```\n\n[Using tool: shell]\n[Tool shell done]\nresult\n[/Tool]\n"
	var requests atomic.Int32
	projects := []models.Project{{ID: "byte-a", Name: "A"}, {ID: "byte-b", Name: "B"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/chat/output/byte-exec" {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(models.DisplayTail{Text: earlier, Revision: "test"})
			return
		}
		if r.URL.Path != "/chat" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		project := r.URL.Query().Get("project_id")
		history := []models.Execution{{ID: "byte-exec", TaskID: "byte-task", Status: models.ExecCompleted, PromptSent: "Question", Output: latest, StartedAt: time.Unix(1, 0), DisplayTail: &models.DisplayTail{Before: 123, Revision: "test"}}}
		if project == "byte-b" {
			history = nil
		}
		w.Header().Set("Content-Type", "text/html")
		if r.Header.Get("HX-Request") == "true" {
			_ = ChatContent(nil, history, project, nil, nil, nil, false, false, 5).Render(r.Context(), w)
		} else {
			_ = Chat(projects, project, nil, history, nil, nil, nil, false, false, 5).Render(r.Context(), w)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id=byte-a", "byte-window", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1200, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("latest output rendered", `String(!!document.querySelector('.chat-stream-content')?._outputWindowSeed && document.getElementById('chat-messages').dataset.transcriptHydrating!=='true')`, "true")
		if requests.Load() != 0 {
			t.Fatal("opening fetched older content")
		}
		b.evaluate(`document.getElementById('chat-messages').scrollTop=0; 'scrolled'`)
		b.waitFor("automatic older output", `String(document.querySelector('.chat-stream-content').getAttribute('data-raw-content').includes('EARLIER_UNIQUE'))`, "true")
		b.waitFor("cursor exhausted", `document.querySelector('.chat-stream-content').dataset.outputWindow`, "")
		b.waitFor("original seed retained", `String(!document.querySelector('.chat-stream-content')._outputWindowSeed.text.includes('EARLIER_UNIQUE'))`, "true")
		// Simulate the separate execution-history loader prepending an older
		// turn. It must not sneak into a cache whose initial view had one turn.
		b.evaluate(`(() => {const m=document.getElementById('chat-messages');const p=m.querySelector('[data-execution-pair]').cloneNode(true);p.id='older-pair';p.dataset.execId='older-exec';p.querySelector('.chat-stream-content').setAttribute('data-raw-content','OLDER_TURN_UNIQUE');m.prepend(p);return 'older loaded';})()`)
		b.evaluate(`window.openVibelyNavigate('/chat?project_id=byte-b'); 'navigating'`)
		b.waitFor("other thread", `document.getElementById('chat-page-root').dataset.projectId`, "byte-b")
		b.waitFor("cache excludes browsed history", `String(Array.from(window.openVibelyThreadCache.pages.values()).some(p=>p.sources.some(s=>s.text.includes('LATEST_END'))) && !Array.from(window.openVibelyThreadCache.pages.values()).some(p=>p.sources.some(s=>(s.text.includes('EARLIER_UNIQUE') || s.text.includes('OLDER_TURN_UNIQUE'))) || p.virtual.some(v=>v.source.includes('EARLIER_UNIQUE'))))`, "true")
		b.evaluate(`window.openVibelyNavigate('/chat?project_id=byte-a'); 'returning'`)
		b.waitFor("latest window restored", `String(document.getElementById('chat-page-root').dataset.projectId==='byte-a' && !!document.querySelector('.chat-stream-content')?.dataset.outputWindow && !document.querySelector('.chat-stream-content').getAttribute('data-raw-content').includes('EARLIER_UNIQUE'))`, "true")
	})
}

// Opt-in measurement of the loading policy in the same browser build. Repository
// comparisons against main are separate; this isolates full vs bounded source.
func TestBrowserPerformance_ByteWindowMeasurement(t *testing.T) {
	path := os.Getenv("OV_BROWSER_OUTPUT")
	if path == "" {
		t.Skip("set OV_BROWSER_OUTPUT to a display fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	full := string(raw)
	items := transcript.ParseDisplay(full)
	start, size := len(items), 0
	for start > 0 {
		item := items[start-1]
		n := len(item.Text) + len(item.Prefix) + len(item.Suffix)
		if size > 0 && size+n > 1024*1024 {
			break
		}
		size += n
		start--
	}
	latest := transcript.DisplayText(items[start:])
	for run := 0; run < 3; run++ {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("bounded=%v/run=%d", bounded, run), func(t *testing.T) {
				source := full
				var tail *models.DisplayTail
				if bounded {
					source = latest
					tail = &models.DisplayTail{Before: 1, Revision: "benchmark"}
				}
				projects := []models.Project{{ID: "measure-a", Name: "A"}, {ID: "measure-b", Name: "B"}}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if static.ServeAsset(w, r) {
						return
					}
					if r.URL.Path != "/chat" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					project := r.URL.Query().Get("project_id")
					history := []models.Execution{{ID: "measure-exec", TaskID: "measure-task", Status: models.ExecCompleted, PromptSent: "Question", Output: source, StartedAt: time.Unix(1, 0), DisplayTail: tail}}
					if project == "measure-b" {
						history = nil
					}
					w.Header().Set("Content-Type", "text/html")
					if r.Header.Get("HX-Request") == "true" {
						_ = ChatContent(nil, history, project, nil, nil, nil, false, false, 5).Render(r.Context(), w)
					} else {
						_ = Chat(projects, project, nil, history, nil, nil, nil, false, false, 5).Render(r.Context(), w)
					}
				}))
				defer server.Close()
				runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL+"/chat?project_id=measure-a", "window-measure", func(b *composerFocusCDP) {
					b.waitForTimeout("initial visible window", `String(document.getElementById('chat-messages')?.dataset.transcriptHydrating!=='true' && !!document.querySelector('.chat-stream-content')?.childElementCount && !window._chatContentRenderActive && !window._chatContentRenderQueue?.length)`, "true", 30*time.Second)
					initialMS := b.evaluate(`String(performance.now())`)
					b.call("HeapProfiler.collectGarbage", nil, nil)
					var initialHeap map[string]float64
					b.call("Runtime.getHeapUsage", nil, &initialHeap)
					b.evaluate(`window.openVibelyNavigate('/chat?project_id=measure-b'); 'away'`)
					b.waitFor("other thread", `document.getElementById('chat-page-root').dataset.projectId`, "measure-b")
					b.call("HeapProfiler.collectGarbage", nil, nil)
					var cachedHeap map[string]float64
					b.call("Runtime.getHeapUsage", nil, &cachedHeap)
					b.evaluate(`window._windowMeasureStart=performance.now();window._windowMeasurePreview=false;document.addEventListener('htmx:beforeSwap',e=>{if(e.detail._ovCachedPreview)window._windowMeasurePreview=true;},true);window.openVibelyNavigate('/chat?project_id=measure-a');'back'`)
					b.waitFor("cached view", `String(window._windowMeasurePreview && document.getElementById('chat-page-root')?.dataset.projectId==='measure-a' && document.getElementById('chat-messages')?.dataset.transcriptHydrating!=='true')`, "true")
					reopenMS := b.evaluate(`String(performance.now()-window._windowMeasureStart)`)
					result, _ := json.Marshal(map[string]any{"bounded": bounded, "run": run + 1, "source_bytes": len(source), "first_ready_ms": initialMS, "reopen_ready_ms": reopenMS, "initial_heap": initialHeap["usedSize"], "cached_heap": cachedHeap["usedSize"]})
					fmt.Println("BROWSER_WINDOW_RESULT", string(result))
				})
			})
		}
	}
}

func TestBrowserFunctional_ByteWindowToolStateAndTouch(t *testing.T) {
	for _, touch := range []bool{false, true} {
		t.Run(fmt.Sprint(touch), func(t *testing.T) {
			chrome := chatNavigationChromePath(t)
			latest := "[Using tool: shell | latest]\n[Tool shell done]\n" + strings.Repeat("latest output line\n", 200) + "[/Tool]\n"
			earlier := "[Using tool: shell | earlier]\n[Tool shell done]\nEARLIER_RESULT\n[/Tool]\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/chat/output/state-exec" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(models.DisplayTail{Text: earlier, Revision: "state"})
					return
				}
				if r.URL.Path != "/chat" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				history := []models.Execution{{ID: "state-exec", TaskID: "state-task", Status: models.ExecCompleted, PromptSent: "Question", Output: latest, StartedAt: time.Unix(1, 0), DisplayTail: &models.DisplayTail{Before: 123, Revision: "state"}}}
				_ = Chat([]models.Project{{ID: "state-project", Name: "State"}}, "state-project", nil, history, nil, nil, nil, false, false, 5).Render(r.Context(), w)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id=state-project", "byte-state", func(b *composerFocusCDP) {
				b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1200, "height": 1100, "deviceScaleFactor": 1, "mobile": false}, nil)
				b.waitFor("display prefs ready", `String(!!window.openVibelyDisplay && document.readyState==='complete')`, "true")
				b.evaluate(`localStorage.setItem('openvibely.outputDetail','full');'full'`)
				b.call("Page.navigate", map[string]any{"url": server.URL + "/chat?project_id=state-project&full=1"}, nil)
				b.waitFor("full output mode", `String(document.readyState==='complete' && location.search.includes('full=1') && openVibelyDisplay.get('output')==='full')`, "true")
				b.waitFor("tool ready", `String(!!document.querySelector('.stream-tool-output-toggle') && document.getElementById('chat-messages').dataset.transcriptHydrating!=='true')`, "true")
				if touch {
					b.waitFor("short window cannot scroll", `String(document.getElementById('chat-messages').scrollHeight<=document.getElementById('chat-messages').clientHeight)`, "true")
					b.evaluate(`(() => { const m=document.getElementById('chat-messages'); for(const [type,y] of [['touchstart',100],['touchmove',150]]) {const e=new Event(type,{bubbles:true});Object.defineProperty(e,'touches',{value:[{clientY:y}]});m.dispatchEvent(e);}return 'touch';})()`)
				} else {
					b.evaluate(`document.getElementById('chat-messages').dataset.transcriptHydrating='true';document.querySelector('.stream-tool-output-toggle').click();'expanded'`)
					b.waitFor("output laid out", `String(document.querySelector('[data-tool-row="out"]').scrollHeight > document.querySelector('[data-tool-row="out"]').clientHeight+100)`, "true")
					b.evaluate(`(() => {const out=document.querySelector('[data-tool-row="out"]');window.savedToolID=out.dataset.toolRenderId;out.scrollTop=50;out.dispatchEvent(new Event('scroll'));return 'position';})()`)
					b.waitFor("output scrolled", `String(Math.abs(document.querySelector('[data-tool-row="out"]').scrollTop-50)<2)`, "true")
					b.evaluate(`(() => {const m=document.getElementById('chat-messages');m.dataset.transcriptHydrating='false';m.scrollTop=0;m.dispatchEvent(new WheelEvent('wheel',{deltaY:-100,bubbles:true}));return 'load';})()`)
				}
				b.waitFor("older tool loaded", `String(document.querySelector('.chat-stream-content').getAttribute('data-raw-content').includes('EARLIER_RESULT') && !document.querySelector('.chat-stream-content').dataset.outputWindow)`, "true")
				if !touch {
					b.waitFor("expanded tool preserved", `String(Array.from(document.querySelectorAll('.stream-tool')).some(t=>t.dataset.toolRenderId===window.savedToolID && t.querySelector('.stream-tool-output-toggle')?.getAttribute('aria-expanded')==='true' && Math.abs(t.querySelector('[data-tool-row="out"]').scrollTop-50)<2))`, "true")
				}
			})
		})
	}
}

func TestBrowserFunctional_ByteWindowStaleCursorRecovery(t *testing.T) {
	for _, scenario := range []string{"retry", "complete", "conflict-again"} {
		t.Run(scenario, func(t *testing.T) {
			chrome := chatNavigationChromePath(t)
			oldText := "OBSOLETE_TEXT\n\n" + strings.Repeat("Latest paragraph.\n\n", 120)
			newText := "REFRESHED_TEXT\n\n" + strings.Repeat("Latest paragraph.\n\n", 120)
			var requests, refreshes, retries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/chat/output/stale-exec" {
					requests.Add(1)
					if r.URL.Query().Get("project_id") != "stale-project" {
						http.Error(w, "wrong project", http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Query().Get("before") {
					case "123":
						http.Error(w, "stale cursor", http.StatusConflict)
					case "0":
						refreshes.Add(1)
						tail := models.DisplayTail{Text: newText, Revision: "fresh", Before: 456}
						if scenario == "complete" {
							tail.Before = 0
						}
						_ = json.NewEncoder(w).Encode(tail)
					case "456":
						retries.Add(1)
						if scenario == "conflict-again" || r.URL.Query().Get("revision") != "fresh" {
							http.Error(w, "changed again", http.StatusConflict)
							return
						}
						_ = json.NewEncoder(w).Encode(models.DisplayTail{Text: "FRESH_EARLIER\n\n", Revision: "fresh"})
					default:
						http.Error(w, "unexpected cursor", http.StatusBadRequest)
					}
					return
				}
				if r.URL.Path != "/chat" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				history := []models.Execution{{ID: "stale-exec", TaskID: "stale-task", Status: models.ExecCompleted, PromptSent: "Question", Output: oldText, StartedAt: time.Unix(1, 0), DisplayTail: &models.DisplayTail{Before: 123, Revision: "old"}}}
				_ = Chat([]models.Project{{ID: "stale-project", Name: "Stale"}}, "stale-project", nil, history, nil, nil, nil, false, false, 5).Render(r.Context(), w)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id=stale-project", "stale-window", func(b *composerFocusCDP) {
				b.waitFor("initial window", `String(!!document.querySelector('.chat-stream-content')?._outputWindowSeed && document.getElementById('chat-messages').dataset.transcriptHydrating!=='true')`, "true")
				b.evaluate(`(() => {const m=document.getElementById('chat-messages');m.scrollTop=0;window.cursorAnchor=()=>Array.from(document.querySelectorAll('.chat-stream-content p')).find(p=>p.textContent==='Latest paragraph.')?.getBoundingClientRect().top;window.cursorAnchorTop=window.cursorAnchor();m.dispatchEvent(new WheelEvent('wheel',{deltaY:-100,bubbles:true}));return 'load';})()`)
				b.waitFor("fresh replacement and cache seed", `String((()=>{const n=document.querySelector('.chat-stream-content');const text=n.getAttribute('data-raw-content');return text.includes('REFRESHED_TEXT') && !text.includes('OBSOLETE_TEXT') && n._outputWindowSeed.text.includes('REFRESHED_TEXT') && !n._outputWindowSeed.text.includes('FRESH_EARLIER')})())`, "true")
				expectedRequests := int32(3)
				if scenario == "conflict-again" {
					b.waitFor("bounded retry finished", `String(performance.getEntriesByType('resource').filter(e=>e.name.includes('/chat/output/')).length===3)`, "true")
					b.waitFor("fresh cursor retained", `document.querySelector('.chat-stream-content').dataset.outputWindow`, "456:fresh")
				} else {
					b.waitFor("cursor exhausted", `document.querySelector('.chat-stream-content').dataset.outputWindow`, "")
					if scenario == "retry" {
						b.waitFor("older content automatically retried", `String(document.querySelector('.chat-stream-content').getAttribute('data-raw-content').startsWith('FRESH_EARLIER'))`, "true")
					} else {
						expectedRequests = 2
					}
				}
				b.waitFor("reading position preserved", `String(Number.isFinite(window.cursorAnchorTop) && Math.abs(window.cursorAnchor()-window.cursorAnchorTop)<3)`, "true")
				if requests.Load() != expectedRequests || refreshes.Load() != 1 || retries.Load() != expectedRequests-2 {
					t.Fatalf("requests=%d refreshes=%d retries=%d", requests.Load(), refreshes.Load(), retries.Load())
				}
			})
		})
	}
}
