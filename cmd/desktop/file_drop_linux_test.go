//go:build linux

package main

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/components"
)

// Uses a GTK file source and XTest pointer events to exercise native URI-list
// delivery to WebKit, the production composer, multipart upload, and preview.
// Always use a private Xvfb display so pointer automation cannot interact with
// the user's desktop or trigger Wayland remote-control permission prompts.
func TestNativeLinuxComposerPNGDrop(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("set OPENVIBELY_RUN_NATIVE_UI=1 with Xvfb and GTK3/WebKit installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	flags, err := exec.CommandContext(ctx, "pkg-config", "--cflags", "--libs", "webkit2gtk-4.1", "xtst", "x11").CombinedOutput()
	if err != nil {
		t.Fatalf("native drag test dependencies: %v: %s", err, flags)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "file-drop-check")
	args := append([]string{"testdata/linux_file_drop_check.c", "-o", binary}, strings.Fields(string(flags))...)
	if output, err := exec.CommandContext(ctx, "cc", args...).CombinedOutput(); err != nil {
		t.Fatalf("compile native drag test: %v: %s", err, output)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "native-drop.png")
	if err := os.WriteFile(filename, picture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var composer bytes.Buffer
	if err := components.ChatInputForm(components.ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", PostEndpoint: "/chat/send", TargetID: "chat-messages"}).Render(ctx, &composer); err != nil {
		t.Fatal(err)
	}
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/attachments" {
			if r.Method != http.MethodPost {
				http.Error(w, "expected POST", 400)
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "multipart", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			files := r.MultipartForm.File["files"]
			if len(files) != 1 {
				t.Errorf("got %d files", len(files))
				http.Error(w, "file count", 400)
				return
			}
			file, err := files[0].Open()
			if err != nil {
				t.Error(err)
				http.Error(w, "open", 400)
				return
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || files[0].Filename != "native-drop.png" || !bytes.Equal(data, picture.Bytes()) {
				t.Errorf("PNG upload mismatch: name=%q, bytes=%d, err=%v", files[0].Filename, len(data), err)
				http.Error(w, "PNG mismatch", 400)
				return
			}
			uploads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"session_id": "native-drop-session", "attachments": []map[string]any{{"filename": "native-drop.png", "size": len(data)}}})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><html><head><style>
.hidden {display:none} svg {width:16px;height:16px} #message-input {position:fixed;left:20px;top:60px;width:500px;height:120px} [id$="-drop-overlay"] {pointer-events:none} #outside {position:fixed;left:20px;top:300px;width:500px;height:150px;z-index:100}
</style></head><body><div id="chat-messages"></div>%s<div id="outside">Outside composer</div><script>
(function() {
 const report = text => window.webkit.messageHandlers.test.postMessage(text);
 window.alert = text => report('fail: '+text);
 window.addEventListener('error', event => report('fail: '+event.message));
 document.addEventListener('dragenter', event => report('dragenter: '+event.target.id+' types='+Array.from(event.dataTransfer.types)), true);
 document.addEventListener('drop', event => report('drop: '+event.target.id+' files='+event.dataTransfer.files.length+' types='+Array.from(event.dataTransfer.types)+' items='+Array.from(event.dataTransfer.items).map(item=>item.kind+':'+item.type)+' uri='+event.dataTransfer.getData('text/uri-list')), true);
 const mode = new URLSearchParams(location.search).get('mode');
 const expectUpload = mode === 'composer';
 let trustedDrag = false, selectedFile = false, sawOverlay = false;
 document.getElementById(mode === 'outside' ? 'outside' : 'message-input').addEventListener('dragenter', event => { trustedDrag = event.isTrusted; });
 document.getElementById('message-input').addEventListener('dragover', () => requestAnimationFrame(() => {
  sawOverlay ||= !document.getElementById('chat-form-drop-overlay').classList.contains('hidden');
 }));
 document.querySelector('input[type="file"]').addEventListener('change', event => { selectedFile = event.isTrusted; });
 const timer = setInterval(() => {
  const row = document.querySelector('[data-pending-attachment-filename="native-drop.png"]');
  if (!expectUpload) {
   if (!window._ovNativeDropTestFinished) return;
   clearInterval(timer);
   report(trustedDrag && !row && !selectedFile ? 'pass' : 'fail: outside/internal drag selected a file or never reached its target');
   return;
  }
  if (!row) return;
  clearInterval(timer);
  const session = document.querySelector('input[name="attachment_session_id"]').value;
  report(trustedDrag && selectedFile && sawOverlay && row.getClientRects().length && session === 'native-drop-session' ? 'pass' : 'fail: missing native drag, overlay, file selection, visible preview, or session');
 }, 50);
 window.addEventListener('load', () => setTimeout(() => report('ready'), 200));
})();</script></body></html>`, composer.String())
	}))
	defer server.Close()
	for _, mode := range []string{"composer", "outside", "internal"} {
		t.Run(mode, func(t *testing.T) {
			uploads.Store(0)
			cmd := exec.CommandContext(ctx, "xvfb-run", "-a", "-s", "-screen 0 1280x800x24 -nolisten tcp", binary, server.URL+"?mode="+mode, filename, mode)
			cmd.Env = append(os.Environ(), "GDK_BACKEND=x11")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native PNG drag: %v: %s", err, output)
			}
			want := int32(0)
			if mode == "composer" {
				want = 1
			}
			if got := uploads.Load(); got != want {
				t.Fatalf("uploads = %d, want %d", got, want)
			}
		})
	}
}
