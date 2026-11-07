package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// Linux covers its GTK chooser workaround in cmd/desktop. These scenarios use
// the Wails WKWebView/WebView2 host and each platform's native file-drop path.
func TestNativeComposerFileDropsInWails(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Wails' WebView2 composition host does not forward native file drops.
		// Re-enable after upgrading to a release containing this upstream fix:
		// https://github.com/wailsapp/wails/pull/6566
		t.Skip("Windows composition-hosted file drops require Wails PR #6566")
	}
	if testing.Short() || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1 and a graphical session")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("Linux uses TestNativeLinuxComposerPNGDrop")
	}
	runner := buildNativeChatHost(t)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, filename string
		content        []byte
		outside        bool
	}{
		{"png-composer", "native-drop.png", picture.Bytes(), false},
		{"text-composer", "native-drop.txt", []byte("native desktop attachment\n"), false},
		{"png-outside", "native-drop.png", picture.Bytes(), true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), scenario.filename)
			if err := os.WriteFile(filename, scenario.content, 0600); err != nil {
				t.Fatal(err)
			}
			var composer, page bytes.Buffer
			if err := components.ChatInputForm(components.ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", PostEndpoint: "/chat/send", TargetID: "chat-messages"}).Render(context.Background(), &composer); err != nil {
				t.Fatal(err)
			}
			if err := layout.Base("Native file drop", nil, "").Render(context.Background(), &page); err != nil {
				t.Fatal(err)
			}
			guard := regexp.MustCompile(`(?s)<script id="file-drop-navigation-guard">.*?</script>`).FindString(page.String())
			if guard == "" {
				t.Fatal("missing production file navigation guard")
			}
			config, err := json.Marshal(map[string]any{"filename": scenario.filename, "size": len(scenario.content), "outside": scenario.outside})
			if err != nil {
				t.Fatal(err)
			}
			var bridge string
			var bridgeMu sync.RWMutex
			var uploads atomic.Int32
			results := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/native-test-bridge":
					body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
					if err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					bridgeMu.Lock()
					bridge = string(body)
					bridgeMu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				case "/browser-result":
					select {
					case results <- r.URL.Query().Get("status"):
					default:
					}
					w.WriteHeader(http.StatusNoContent)
				case "/chat/attachments":
					if r.Method != http.MethodPost {
						http.Error(w, "expected POST", 400)
						return
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						http.Error(w, "invalid multipart", 400)
						return
					}
					defer r.MultipartForm.RemoveAll()
					files := r.MultipartForm.File["files"]
					if len(files) != 1 {
						t.Errorf("upload contains %d files", len(files))
						http.Error(w, "file count", 400)
						return
					}
					file, err := files[0].Open()
					if err != nil {
						t.Error(err)
						http.Error(w, "open file", 400)
						return
					}
					defer file.Close()
					content, err := io.ReadAll(file)
					if err != nil || files[0].Filename != scenario.filename || !bytes.Equal(content, scenario.content) {
						t.Errorf("uploaded file mismatch: name=%q, bytes=%d, err=%v", files[0].Filename, len(content), err)
						http.Error(w, "file mismatch", 400)
						return
					}
					uploads.Add(1)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"session_id": "native-drop-session", "attachments": []map[string]any{{"filename": scenario.filename, "size": len(content)}}})
				case "/":
					bridgeMu.RLock()
					script := bridge
					bridgeMu.RUnlock()
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, `<!doctype html><html><head>%s<style>
.hidden {display:none} svg {width:16px;height:16px} #message-input {position:fixed;left:20px;top:60px;width:500px;height:120px} [id$="-drop-overlay"] {pointer-events:none} #outside {position:fixed;left:20px;top:300px;width:500px;height:150px;z-index:100}
</style><script>%s</script></head><body><div id="chat-messages"></div>%s<div id="outside">Outside composer</div><script>
(async function() {
 const config = %s;
 const report = status => fetch('/browser-result?status='+encodeURIComponent(status));
 window.alert = text => report('fail: '+text);
 window.addEventListener('error', event => report('fail: '+event.message));
 const assert = (ok, text) => { if(!ok) throw Error(text); };
 const waitFor = async (predicate, label) => {
  const deadline = performance.now()+15000;
  while (!predicate()) { if(performance.now()>deadline) throw Error('Timed out: '+label); await new Promise(r=>setTimeout(r,30)); }
 };
 try {
  const target = document.getElementById(config.outside ? 'outside' : 'message-input');
  let entered = false, dropped = false, sawOverlay = false, nativeFiles = [];
  target.addEventListener('dragenter', event => { entered = event.isTrusted; });
  target.addEventListener('dragover', () => requestAnimationFrame(() => {
   sawOverlay ||= !document.getElementById('chat-form-drop-overlay').classList.contains('hidden');
  }));
  target.addEventListener('drop', event => {
   dropped = event.isTrusted;
   nativeFiles = Array.from(event.dataTransfer.files).map(file=>({name:file.name,size:file.size}));
  });
  await new Promise(resolve => { if(document.readyState==='complete') resolve(); else window.addEventListener('load',resolve,{once:true}); });
  await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
  const rect = target.getBoundingClientRect();
  await window.nativeTestDrop.postMessage({x:rect.left+80,y:rect.top+50});
  await waitFor(()=>entered && dropped,'trusted native dragenter and drop');
  assert(nativeFiles.length===1 && nativeFiles[0].name===config.filename && nativeFiles[0].size===config.size,'native drop lost the file');
  if (!config.outside) await waitFor(()=>document.querySelector('[data-pending-attachment]'),'attachment preview');
  await new Promise(r=>setTimeout(r,300));
  const rows = document.querySelectorAll('[data-pending-attachment]');
  if(config.outside) {
   assert(rows.length===0,'outside drop attached a file');
  } else {
   assert(sawOverlay,'drop indicator never appeared');
   assert(rows.length===1 && rows[0].getClientRects().length,'missing or duplicate visible preview');
   assert(rows[0].getAttribute('data-pending-attachment-filename')===config.filename,'wrong preview filename');
   assert(document.querySelector('input[name="attachment_session_id"]').value==='native-drop-session','missing attachment session');
  }
  await report('pass');
 } catch(error) { await report('fail: '+error.message); }
})();</script></body></html>`, guard, script, composer.String(), config)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, runner, server.URL)
			cmd.Env = append(os.Environ(), "OPENVIBELY_NATIVE_DROP_FILE="+filename)
			testutil.ConfigureNativeBrowserStorage(t, cmd)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			exited := false
			defer func() {
				if !exited {
					if err := testutil.StopNativeBrowser(cmd, done); err != nil {
						t.Errorf("stop native host: %v", err)
					}
				}
			}()
			var result string
			select {
			case result = <-results:
				if err := testutil.StopNativeBrowser(cmd, done); err != nil {
					t.Errorf("stop native host: %v", err)
				}
				exited = true
			case err := <-done:
				exited = true
				t.Fatalf("native host exited: %v\n%s", err, output.String())
			case <-ctx.Done():
				// Stop before reading the process buffer or cleaning up its profile.
				_ = testutil.StopNativeBrowser(cmd, done)
				exited = true
				t.Fatalf("native file drop timed out\n%s", output.String())
			}
			if result != "pass" {
				t.Fatalf("%s\n%s", result, output.String())
			}
			want := int32(1)
			if scenario.outside {
				want = 0
			}
			if got := uploads.Load(); got != want {
				t.Fatalf("uploads=%d, want %d", got, want)
			}
		})
	}
}
