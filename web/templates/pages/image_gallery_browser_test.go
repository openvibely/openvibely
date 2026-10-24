package pages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_SharedImageGallery(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "images", Name: "Images"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wails/runtime.js" {
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, `export const Events={On:()=>{},Emit:(name,data)=>{window.nativeControlEvents=window.nativeControlEvents||{};window.nativeControlEvents[name]=data;}};`)
			return
		}
		if static.ServeAsset(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "/download") || strings.Contains(r.URL.Path, "/pending/") {
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1200"><rect width="1600" height="1200" fill="teal"/></svg>`)
			return
		}
		if r.URL.Path == "/chat/attachments" {
			json.NewEncoder(w).Encode(map[string]any{"session_id": "01234567890123456789012345678901", "attachments": []map[string]any{{"filename": "draft.svg", "size": 100, "media_type": "image/svg+xml", "session_id": "01234567890123456789012345678901"}}})
			return
		}
		if r.URL.Path == "/tasks" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("attachment_session_id") != "01234567890123456789012345678901" {
				http.Error(w, "missing uploads", 400)
				return
			}
			w.Header().Set("HX-Retarget", "#main-content")
			w.Header().Set("HX-Reswap", "innerHTML")
			w.Header().Set("X-Created-Task-ID", "created")
			task := &models.Task{ID: "created", ProjectID: project.ID, Title: "Created with image", Status: models.StatusPending, Category: models.CategoryActive}
			_ = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "attachments", nil).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/tasks/created/attachments" {
			_ = components.AttachmentListOnly(nil, project.ID, []models.ChatAttachment{{ID: "sent", FileName: "draft.svg", MediaType: "image/svg+xml"}}).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/new" {
			_ = NewTask([]models.Project{project}, &project, nil, nil).Render(r.Context(), w)
			return
		}
		if r.URL.Path != "/gallery" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if r.URL.Query().Get("delayed_titlebar") == "1" {
				io.WriteString(w, `<script>window.delayedTitlebar=document.getElementById('desktop-project-titlebar');window.delayedTitlebar.remove();</script>`)
			}
			if err := components.AttachmentList([]models.Attachment{{ID: "one", FileName: "one.svg", MediaType: "image/svg+xml"}, {ID: "two", FileName: "two.svg", MediaType: "image/svg+xml"}, {ID: "document", FileName: "notes.txt", MediaType: "text/plain"}}, project.ID, "task").Render(ctx, w); err != nil {
				return err
			}
			return components.ChatBubbleWithAttachments("User", "Message", []models.ChatAttachment{{ID: "three", FileName: "message.svg", MediaType: "image/svg+xml"}}, project.ID).Render(ctx, w)
		})
		_ = layout.Base("Gallery", []models.Project{project}, project.ID).Render(templ.WithChildren(layout.WithDesktopMode(r.Context(), true), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/gallery?delayed_titlebar=1", "delayed-native-bridge", func(b *composerFocusCDP) {
		b.waitFor("native runtime ready before titlebar", `String(!!window.wails && !document.getElementById('desktop-project-titlebar'))`, "true")
		b.evaluate(`document.body.prepend(window.delayedTitlebar);document.getElementById('image-gallery').showModal();'opened'`)
		b.waitFor("delayed titlebar still hides controls", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "true")
		b.evaluate(`document.getElementById('image-gallery').close();'closed'`)
		b.waitFor("delayed titlebar restores controls", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "false")
	})
	runComposerFocusCDP(t, chrome, server.URL+"/gallery", "native-visibility-recovery", func(b *composerFocusCDP) {
		b.waitFor("native bridge ready", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "false")
		for _, failure := range []string{"throw", "reject", "cancel"} {
			b.evaluate(`window.originalVisibilityEmit=window.wails.Events.Emit;window.failVisibilityOnce=true;window.wails.Events.Emit=(name,data)=>{if(name==='desktop:controls-visibility' && window.failVisibilityOnce){window.failVisibilityOnce=false;` + map[string]string{
				"throw":  `throw new Error('bridge unavailable');`,
				"reject": `return Promise.reject(new Error('bridge unavailable'));`,
				"cancel": `return Promise.resolve(true);`,
			}[failure] + `}return window.originalVisibilityEmit(name,data);};document.getElementById('image-gallery').showModal();'opened'`)
			b.waitFor("retry hides controls after "+failure, `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "true")
			b.evaluate(`window.wails.Events.Emit=window.originalVisibilityEmit;document.getElementById('image-gallery').close();'closed'`)
			b.waitFor("close restores controls", `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "false")
		}
		b.evaluate(`window.wails.Events.Emit=(name,data)=>name==='desktop:controls-visibility' && data.hidden ? new Promise((resolve,reject)=>{window.rejectVisibility=reject;}) : window.originalVisibilityEmit(name,data);document.getElementById('image-gallery').showModal();'opened'`)
		b.waitFor("hide pending", `String(!!window.rejectVisibility)`, "true")
		b.evaluate(`document.getElementById('image-gallery').close();'closed'`)
		b.waitFor("close supersedes pending hide", `String(!document.getElementById('image-gallery').open && window.nativeControlEvents['desktop:controls-visibility'].hidden===false)`, "true")
		b.evaluate(`window.rejectVisibility(new Error('late failure'));window.wails.Events.Emit=window.originalVisibilityEmit;window.nativeControlEvents['desktop:controls-visibility'].hidden=true;window.dispatchEvent(new Event('focus'));'focused'`)
		b.waitFor("focus reconciles native visibility", `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "false")
	})
	runComposerFocusCDP(t, chrome, server.URL+"/gallery", "image-gallery", func(b *composerFocusCDP) {
		b.waitFor("gallery entries", `String(!!document.querySelector("#attachment-list [data-image-gallery-item]") && !!window.imageGallery)`, "true")
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			b.waitFor("native controls initially visible", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "false")
		}
		b.waitFor("chat image link fits thumbnail", `String((()=>{const img=document.querySelector('[data-chat-attachment-image]'),link=img.closest('a');return Math.abs(link.getBoundingClientRect().width-img.getBoundingClientRect().width)<1})())`, "true")
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			b.evaluate(`window.savedGallery=document.getElementById('image-gallery');window.savedGallery.remove();'removed'`)
			b.waitFor("removing preview restores native controls", `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "false")
			b.evaluate(`const replacement=document.createElement('dialog');replacement.id='image-gallery';document.body.append(replacement);replacement.showModal();'replaced'`)
			b.waitFor("replacement preview hides native controls", `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "true")
			b.evaluate(`document.getElementById('image-gallery').close();'closed'`)
			b.waitFor("replacement close restores native controls", `String(window.nativeControlEvents['desktop:controls-visibility'].hidden)`, "false")
			b.evaluate(`document.getElementById('image-gallery').replaceWith(window.savedGallery);delete window.savedGallery;'restored'`)
		}

		for _, selector := range []string{"#attachment-list [data-image-gallery-item]", "#attachment-list [data-file-preview]", "a:has([data-chat-attachment-image])"} {
			b.click(selector)
			b.waitFor("preview open", `String(document.getElementById('image-gallery').open)`, "true")
			b.click("[data-gallery-close]")
			b.waitFor("pointer preview leaves no active link", `String(!document.getElementById('image-gallery').open && !document.activeElement.matches('[data-image-gallery-item],[data-file-preview]'))`, "true")
		}
		b.evaluate(`const controls=document.createElement('div');controls.id='focus-preview-controls';controls.innerHTML='<button id="focus-files" data-files-panel="/attachments/document/download">Files</button><div class="file-html-canvas"><iframe hidden src="/attachments/document/download"></iframe><button id="focus-expand">Expand</button></div>';document.body.prepend(controls);'ready'`)
		for _, selector := range []string{"#focus-files", "#focus-expand"} {
			// Alternate activation modes to catch state inherited from a previous preview.
			for _, pointer := range []bool{false, true, false} {
				if pointer {
					b.click(selector)
				} else {
					b.evaluate(`document.querySelector('` + selector + `').focus();document.querySelector('` + selector + `').click();'activated without pointer'`)
				}
				b.waitFor("button preview opens", `String(document.getElementById('image-gallery').open)`, "true")
				b.click("[data-gallery-close]")
				expected := "true"
				if pointer {
					expected = "false"
				}
				b.waitFor("button preview closes", `String(document.getElementById('image-gallery').open)`, "false")
				b.waitFor("button focus follows current activation", `String(document.activeElement===document.querySelector('`+selector+`'))`, expected)
			}
		}
		b.evaluate(`document.getElementById('focus-preview-controls').remove();'removed'`)
		b.click(`#attachment-list [data-image-gallery-item]`)
		b.waitFor("task gallery loaded", `document.querySelector('[data-gallery-count]').textContent+':'+document.querySelector('[data-gallery-status]').textContent`, "1 / 3:")
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			b.waitFor("original image toolbar and side navigation", `String((()=>{const d=document.getElementById('image-gallery'),a=d.querySelector('[data-gallery-download]'),prev=d.querySelector('[data-gallery-prev]'),stage=d.querySelector('[data-gallery-stage]');return !!a.closest('footer .image-gallery-tools') && !!a.querySelector('svg') && !a.textContent.trim() && !d.querySelector('[data-gallery-thumbnails]') && d.querySelector('[data-result-actions]').hidden && !!prev.closest('.image-gallery-viewer') && getComputedStyle(prev).position==='absolute' && getComputedStyle(stage).marginLeft==='68px';})())`, "true")

			b.waitFor("preview hides native controls", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "true")
			b.waitFor("visibility events carry page and increasing sequence", `String((()=>{const e=window.nativeControlEvents['desktop:controls-visibility'];return e.page===performance.timeOrigin && e.sequence>=2;})())`, "true")
			b.evaluate(`window.previewVisibilitySequence=window.nativeControlEvents['desktop:controls-visibility'].sequence;'saved'`)
		}
		for _, theme := range []string{"dark", "light"} {
			b.evaluate(`document.documentElement.dataset.theme='` + theme + `';document.documentElement.dataset.colorTheme='openvibely-` + theme + `';'set'`)
			for _, control := range []string{"close", "next", "plus"} {
				var point struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
				}
				json.Unmarshal([]byte(b.evaluate(`JSON.stringify((()=>{const r=document.querySelector('[data-gallery-`+control+`]').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})())`)), &point)
				b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
				background := "rgba(0, 0, 0, 0.7)"
				if control == "plus" {
					background = "rgba(0, 0, 0, 0)"
				}
				b.waitFor(theme+" "+control+" single hover layer", `String((()=>{const s=getComputedStyle(document.querySelector('[data-gallery-`+control+`]'));return s.backgroundColor==='`+background+`' && s.backgroundImage==='linear-gradient(rgba(255, 255, 255, 0.12), rgba(255, 255, 255, 0.12))';})())`, "true")
			}
		}
		b.waitFor("fullscreen transparent preview", `String((()=>{const d=document.getElementById('image-gallery'),r=d.getBoundingClientRect();return r.left===0 && r.top===0 && Math.abs(r.width-innerWidth)<1 && Math.abs(r.height-innerHeight)<1 && getComputedStyle(d).backgroundColor==='rgba(0, 0, 0, 0)';})())`, "true")
		b.click(`[data-gallery-image]`)
		b.waitFor("image click keeps preview open", `String(document.getElementById('image-gallery').open)`, "true")
		// Dragging across the preview chrome must not start a selection that
		// can paint over the image, even before zooming.
		var selectionStart struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		json.Unmarshal([]byte(b.evaluate(`JSON.stringify((()=>{const r=document.querySelector('[data-gallery-name]').getBoundingClientRect();return {x:r.left+2,y:r.top+r.height/2}})())`)), &selectionStart)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": selectionStart.X, "y": selectionStart.Y, "button": "left", "clickCount": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": selectionStart.X + 200, "y": selectionStart.Y, "button": "left", "buttons": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": selectionStart.X + 200, "y": selectionStart.Y, "button": "left", "clickCount": 1}, nil)
		b.waitFor("preview does not create text selection", `String(window.getSelection().isCollapsed && document.getElementById('image-gallery').open)`, "true")
		b.waitFor("WebKit preview selection disabled", `getComputedStyle(document.querySelector('[data-gallery-image]')).webkitUserSelect`, "none")

		b.waitFor("arrows outside clipped image stage", `String((()=>{const stage=document.querySelector('[data-gallery-stage]').getBoundingClientRect(), prev=document.querySelector('[data-gallery-prev]').getBoundingClientRect(), next=document.querySelector('[data-gallery-next]').getBoundingClientRect();return prev.right<stage.left && next.left>stage.right && Math.abs(prev.top+prev.height/2-stage.top-stage.height/2)<1;})())`, "true")
		b.evaluate(`window.galleryArrowPositions=Array.from(document.querySelectorAll('.image-gallery-arrow'),el=>{const r=el.getBoundingClientRect();return [r.x,r.y]});'saved'`)
		b.evaluate(`window.galleryBoundaryImage=document.querySelector('[data-gallery-image]');window.galleryBoundaryTransform=window.galleryBoundaryImage.style.transform;'saved'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowLeft"}, nil)
		b.waitFor("ArrowLeft boundary preserves image and zoom", `String(document.querySelector('[data-gallery-image]')===window.galleryBoundaryImage && window.galleryBoundaryImage.style.transform===window.galleryBoundaryTransform)`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowRight"}, nil)
		b.waitFor("next image", `document.querySelector('[data-gallery-name]').textContent`, "two.svg")
		b.click(`[data-gallery-plus]`)
		b.waitFor("icon toolbar", `String(document.querySelectorAll('.image-gallery-tools svg').length===4 && !!document.querySelector('.image-gallery-tools [data-gallery-download][download] svg') && document.querySelector('.image-gallery-tools').textContent.trim()==='')`, "true")
		b.waitFor("zoom", `document.querySelector('[data-gallery-fit]').getAttribute('aria-label')`, "Fit image (150%)")
		b.waitFor("arrows fixed after navigation and zoom", `String(JSON.stringify(Array.from(document.querySelectorAll('.image-gallery-arrow'),el=>{const r=el.getBoundingClientRect();return [r.x,r.y]}))===JSON.stringify(window.galleryArrowPositions))`, "true")
		b.evaluate(`window.galleryBoundaryImage=document.querySelector('[data-gallery-image]');window.galleryBoundaryTransform=window.galleryBoundaryImage.style.transform;'saved'`)

		var point struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		json.Unmarshal([]byte(b.evaluate(`JSON.stringify((()=>{var r=document.querySelector('[data-gallery-stage]').getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2}})())`)), &point)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "clickCount": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X + 30, "y": point.Y + 30, "button": "left", "buttons": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": point.X + 30, "y": point.Y + 30, "button": "left", "clickCount": 1}, nil)
		b.waitFor("image panned", `String(!document.querySelector('[data-gallery-image]').style.transform.startsWith('translate(0px, 0px)'))`, "true")
		b.waitFor("panning does not select preview", `String(window.getSelection().isCollapsed && document.getElementById('image-gallery').open)`, "true")
		b.click(`[data-gallery-fit]`)
		b.waitFor("fit reset", `document.querySelector('[data-gallery-image]').style.transform`, "translate(0px, 0px) scale(1)")
		// A click spanning background and image is retargeted to their shared
		// container. Neither direction should dismiss the preview.
		var imagePoint struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		json.Unmarshal([]byte(b.evaluate(`JSON.stringify((()=>{const r=document.querySelector('[data-gallery-image]').getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2};})())`)), &imagePoint)
		for _, fromImage := range []bool{false, true} {
			startX, startY, endX, endY := 5.0, 5.0, imagePoint.X, imagePoint.Y
			if fromImage {
				startX, startY, endX, endY = endX, endY, startX, startY
			}
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": startX, "y": startY, "button": "left", "clickCount": 1}, nil)
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": endX, "y": endY, "button": "left", "buttons": 1}, nil)
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": endX, "y": endY, "button": "left", "clickCount": 1}, nil)
			b.waitFor("mixed background and image gesture stays open", `String(document.getElementById('image-gallery').open)`, "true")
		}
		json.Unmarshal([]byte(b.evaluate(`JSON.stringify((()=>{const r=document.querySelector('[data-gallery-stage]').getBoundingClientRect();return {x:r.left+2,y:r.top+2};})())`)), &point)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "clickCount": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "clickCount": 1}, nil)
		b.waitFor("empty preview stage dismisses without pointer focus", `String(!document.getElementById('image-gallery').open && !document.querySelector('[data-gallery-download]').hasAttribute('href') && !document.activeElement.hasAttribute('data-image-gallery-item'))`, "true")
		b.click(`#attachment-list [data-image-gallery-item]`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
		b.waitFor("Escape closes pointer preview without restoring focus", `String(!document.getElementById('image-gallery').open && !document.querySelector('[data-gallery-download]').hasAttribute('href') && !document.activeElement.hasAttribute('data-image-gallery-item'))`, "true")
		// Keyboard activation must restore the exact opener after close cleanup.
		b.evaluate(`window.keyboardGalleryOpener=document.querySelector('#attachment-list [data-image-gallery-item]');window.keyboardGalleryOpener.focus();'focused'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("keyboard preview opens", `String(document.getElementById('image-gallery').open)`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
		b.waitFor("keyboard close restores opener after cleanup", `String(!document.getElementById('image-gallery').open && !document.querySelector('[data-gallery-download]').hasAttribute('href') && document.activeElement===window.keyboardGalleryOpener)`, "true")
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			b.waitFor("closing restores native controls", `String(window.nativeControlEvents?.['desktop:controls-visibility']?.hidden)`, "false")
			b.waitFor("restore supersedes hide", `String(window.nativeControlEvents['desktop:controls-visibility'].sequence>window.previewVisibilitySequence)`, "true")
		}
		b.click(`[data-image-name="message.svg"]`)
		b.waitFor("message scoped gallery", `document.querySelector('[data-gallery-count]').textContent`, "1 / 1")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": 5, "y": 5, "button": "left", "clickCount": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 5, "y": 5, "button": "left", "clickCount": 1}, nil)
		b.waitFor("backdrop dismissal", `String(document.getElementById('image-gallery').open)`, "false")
		b.evaluate(`document.querySelector('[data-image-name="message.svg"]').href='/broken';'set'`)
		b.click(`[data-image-name="message.svg"]`)
		b.waitFor("failed image", `document.querySelector('[data-gallery-status]').textContent`, "Image unavailable")
		b.evaluate(`document.querySelector('[data-image-name="message.svg"]').closest('[data-image-gallery-group]').remove();'removed'`)
		b.waitFor("removing opener preserves preview", `String(document.getElementById('image-gallery').open)`, "true")
		b.evaluate(`window.dispatchEvent(new PopStateEvent("popstate")); "away"`)
		b.waitFor("history navigation closes preview", `String(document.getElementById('image-gallery').open)`, "false")
	})
	runComposerFocusCDP(t, chrome, server.URL+"/new?tab=attachments", "draft-gallery", func(b *composerFocusCDP) {
		b.waitFor("composer loaded", `String(!!document.getElementById('task-thread-form-file-input') && typeof window['task-thread-formClearAttachments']==='function')`, "true")
		// Reinitializing a preserved composer must not duplicate its drop handlers.
		b.evaluate(`var original=Array.from(document.scripts).find(s=>s.textContent.includes('var attachmentUploadQueue = Promise.resolve()'));var replacement=document.createElement('script');replacement.textContent=original.textContent;original.replaceWith(replacement);window.composerUploadCount=0;var originalFetch=window.fetch;window.fetch=function(url,options){if(String(url).startsWith('/chat/attachments'))window.composerUploadCount++;return originalFetch.apply(this,arguments)};var dropFiles=new DataTransfer();dropFiles.items.add(new File(['image'],'draft.svg',{type:'image/svg+xml'}));document.getElementById('task-thread-form').dispatchEvent(new DragEvent('drop',{bubbles:true,cancelable:true,dataTransfer:dropFiles}));'dropped'`)
		b.waitFor("single dropped attachment after reinitialization", `String(document.querySelectorAll('#task-thread-form [data-pending-attachment]').length)+':'+window.composerUploadCount`, "1:1")
		b.evaluate(`window['task-thread-formClearAttachments']();'cleared'`)
		b.evaluate(`document.getElementById('task-message-input').value='Keep my draft';document.getElementById('task-message-input').dispatchEvent(new Event('input',{bubbles:true}));var dt=new DataTransfer();dt.items.add(new File(['image'],'draft.svg',{type:'image/svg+xml'}));var input=document.getElementById('task-thread-form-file-input');input.files=dt.files;input.dispatchEvent(new Event('change',{bubbles:true}));'uploaded'`)
		b.waitFor("draft image in panel", `String(!!document.querySelector('#draft-panel-attachments [data-image-gallery-item]'))`, "true")
		b.waitFor("file thumbnail uses standard content inset", `String(Math.abs(document.querySelector("#draft-panel-attachments .attachment-gallery-open img").getBoundingClientRect().top-document.querySelector("#draft-panel-attachments").getBoundingClientRect().top-4)<1)`, "true")

		b.waitFor("draft file row aligns with panel rows", `String((function(){var row=document.querySelector('#draft-panel-attachments .attachment-gallery-row').getBoundingClientRect(), heading=document.querySelector('#tab-attachments h3').getBoundingClientRect(), list=document.querySelector('#draft-panel-attachments').getBoundingClientRect();return Math.abs(row.left-heading.left+12)<1 && Math.abs(row.right-list.right-12)<1 && Math.abs(document.querySelector('#draft-panel-attachments img').getBoundingClientRect().left-heading.left)<1;})())`, "true")
		b.click(`#draft-panel-attachments [data-image-gallery-item]`)
		b.waitFor("draft preview", `document.querySelector('[data-gallery-name]').textContent+':'+document.querySelector('[data-gallery-status]').textContent`, "draft.svg:")
		b.click(`[data-gallery-close]`)
		b.click(`#task-thread-form [data-image-gallery-item]`)
		b.waitFor("composer gallery", `String(document.getElementById('image-gallery').open)`, "true")
		b.click(`[data-gallery-close]`)
		b.waitFor("draft preserved", `document.getElementById('task-message-input').value+':'+document.querySelector('#task-thread-form input[name=attachment_session_id]').value`, "Keep my draft:01234567890123456789012345678901")
		b.evaluate(`window.draftRowSpacing = getComputedStyle(document.querySelector('#draft-panel-attachments .attachment-gallery-open')).gap; 'measured'`)
		b.evaluate(`htmx.ajax('GET','/gallery',{target:'#main-content',swap:'innerHTML'});'away'`)
		b.waitFor("left creation page", `String(!document.getElementById('task-message-input'))`, "true")
		b.evaluate(`htmx.ajax('GET','/new?tab=attachments',{target:'#main-content',select:'#main-content',swap:'innerHTML'});'new'`)
		b.waitFor("restored message with fresh attachments", `String(!!document.getElementById('task-message-input') && document.getElementById('task-message-input').value==='Keep my draft' && document.querySelector('#task-thread-form input[name=attachment_session_id]').value==='' && !document.querySelector('#draft-panel-attachments [data-image-gallery-item]') && !document.querySelector('#task-thread-form [data-pending-attachment]') && !document.querySelector('#main-content .htmx-added, #main-content.htmx-settling'))`, "true")
		b.evaluate(`var dt=new DataTransfer();dt.items.add(new File(['image'],'draft.svg',{type:'image/svg+xml'}));var input=document.getElementById('task-thread-form-file-input');input.files=dt.files;input.dispatchEvent(new Event('change',{bubbles:true}));'uploaded'`)
		b.waitFor("fresh attachment ready", `String(!!document.querySelector('#draft-panel-attachments [data-image-gallery-item]'))`, "true")
		b.click("#task-message-input")
		b.typeText("Send the image")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("sent image remains in saved panel", `String(!!document.querySelector('#attachment-list [data-image-name="draft.svg"]') && !document.getElementById('draft-panel-attachments'))`, "true")
		b.waitFor("consistent attachment spacing", `String(getComputedStyle(document.querySelector('#attachment-list .attachment-gallery-open')).gap===window.draftRowSpacing)`, "true")
		b.waitFor("file thumbnail uses standard content inset", `String(Math.abs(document.querySelector("#attachment-list .attachment-gallery-open img").getBoundingClientRect().top-document.querySelector("#attachment-list").getBoundingClientRect().top-4)<1)`, "true")

		b.waitFor("saved file row aligns with panel rows", `String((function(){var row=document.querySelector('#attachment-list .attachment-gallery-row').getBoundingClientRect(), heading=document.querySelector('#attachment-list').getBoundingClientRect(), list=document.querySelector('#attachment-list').getBoundingClientRect();return Math.abs(row.left-heading.left+12)<1 && Math.abs(row.right-list.right-12)<1 && Math.abs(document.querySelector('#attachment-list img').getBoundingClientRect().left-heading.left)<1;})())`, "true")
		b.click("#attachment-list [data-image-gallery-item]")
		b.waitFor("saved image preview after send", `document.querySelector('[data-gallery-name]').textContent`, "draft.svg")
		b.evaluate(`window.oldAttachmentList=document.getElementById('attachment-list');htmx.ajax('GET','/tasks/created/attachments',{target:'#attachment-list',swap:'outerHTML'});'refresh'`)
		b.waitFor("panel refreshed without dismissing preview", `String(document.getElementById('attachment-list')!==window.oldAttachmentList && !document.querySelector('.htmx-settling') && document.getElementById('image-gallery').open)`, "true")
		b.click("[data-gallery-close]")
		// Composer and panel entry points share the task gallery, without duplicate saved files.
		b.evaluate(`var composer=document.createElement('form');composer.id='task-thread-form';composer.innerHTML='<div data-image-gallery-group><a data-image-gallery-item data-image-name="pending.svg" href="/chat/attachments/pending/session/pending.svg">Pending upload</a></div>';document.getElementById('task-detail-content').append(composer);'ready'`)
		b.click("#task-thread-form [data-image-gallery-item]")
		b.waitFor("composer includes saved task files", `String(document.querySelector('[data-gallery-count]').textContent.endsWith(' / 2'))`, "true")
		b.evaluate(`window.composerGalleryCount=document.querySelector('[data-gallery-count]').textContent;'saved'`)
		b.evaluate(`document.querySelector(document.querySelector('[data-gallery-next]').disabled?'[data-gallery-prev]':'[data-gallery-next]').click();'navigate'`)
		b.waitFor("navigate from pending upload to saved file", `String(document.querySelector('[data-gallery-count]').textContent!==window.composerGalleryCount)`, "true")
		b.click("[data-gallery-close]")
		b.click("#attachment-list [data-image-gallery-item]")
		b.waitFor("panel shares composer gallery", `String(document.querySelector('[data-gallery-count]').textContent.endsWith(' / 2'))`, "true")
		b.click("[data-gallery-close]")
		b.evaluate(`document.getElementById('task-thread-form').remove();'cleared'`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 700, "deviceScaleFactor": 1, "mobile": true}, nil)
		// The workspace ResizeObserver re-lays out the details panel as an overlay after the viewport change.
		b.waitFor("saved page settled into mobile overlay before preview", `String(!document.querySelector('.htmx-settling, .htmx-swapping, .htmx-request') && document.getElementById('task-details-panel').dataset.overlay==='true')`, "true")
		b.click(`#attachment-list [data-image-gallery-item]`)
		b.waitFor("mobile gallery open", `String(document.getElementById('image-gallery').open)`, "true")
		b.waitFor("mobile fits viewport", `String(document.getElementById('image-gallery').getBoundingClientRect().right<=innerWidth && document.getElementById('image-gallery').getBoundingClientRect().bottom<=innerHeight)`, "true")
		b.waitFor("mobile gallery keeps side navigation within viewport", `String((()=>{const stage=document.querySelector('[data-gallery-stage]').getBoundingClientRect(), prev=document.querySelector('[data-gallery-prev]').getBoundingClientRect(), next=document.querySelector('[data-gallery-next]').getBoundingClientRect();return prev.width>0 && next.width>0 && prev.left>=0 && next.right<=innerWidth && prev.right<next.left && Math.abs(prev.top+prev.height/2-stage.top-stage.height/2)<1 && Math.abs(next.top+next.height/2-stage.top-stage.height/2)<1;})())`, "true")
	})
}
