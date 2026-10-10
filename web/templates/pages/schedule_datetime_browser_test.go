package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_ScheduleDateTimePicker(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html data-theme="dark"><head><link rel="stylesheet" href="%s"><link rel="stylesheet" href="%s"></head><body><dialog id="editor"><form id="schedule"><button type="button" id="outside">Outside</button>`, static.URL("app.css"), static.URL("app-utilities.css"))
		if err := components.ScheduleDateTime("2036-02-29T13:44").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `</form></dialog><script>document.getElementById('editor').showModal()</script></body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "schedule-datetime", func(b *composerFocusCDP) {
		b.waitFor("editor ready", `String(!!document.getElementById("editor") && document.getElementById("editor").open && !!window.openScheduleDateTime)`, "true")
		b.waitFor("readable initial value", `document.querySelector('[data-run-at-picker]').value`, "Feb 29, 2036 · 1:44 PM")
		b.click(`[data-run-at-picker]`)
		b.waitFor("picker opens inside modal", `String(document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.dispatchEvent(new CustomEvent('htmx:beforeSwap',{detail:{target:document.getElementById('outside')}})); 'refreshed'`)
		b.waitFor("unrelated refresh keeps picker open", `String(document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.waitFor("leap day selected", `document.querySelector('[data-date="2036-02-29"]').getAttribute('aria-pressed')`, "true")
		b.waitFor("existing time restored", `document.querySelector('[data-time-column="hour"] [aria-selected="true"]').textContent + ':' + document.querySelector('[data-time-column="minute"] [aria-selected="true"]').textContent + document.querySelector('[data-time-column="period"] [aria-selected="true"]').textContent`, "01:44PM")
		b.click(`[data-time-column="hour"] [data-value="12"]`)
		b.click(`[data-time-column="period"] [data-value="0"]`)
		b.evaluate(`document.querySelector('[data-time-column="minute"]').focus(); 'focused'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Home"}, nil)
		b.click(`[data-done]`)
		b.waitFor("midnight serialized in local format", `new FormData(document.getElementById('schedule')).get('run_at')`, "2036-02-29T00:00")
		b.waitFor("done closes picker only", `String(!document.querySelector('.schedule-datetime-popup').matches(':popover-open') && document.getElementById('editor').open && document.activeElement.matches('[data-run-at-picker]'))`, "true")
		b.click(`[data-run-at-picker]`)
		b.evaluate(`document.querySelectorAll('[data-month-step], [data-today], [data-done]').forEach(button=>button.replaceWith(button.cloneNode(true))); 'controls refreshed'`)
		b.click(`[data-month-step="-1"]`)
		b.waitFor("previous month", `document.querySelector('[data-month-title]').textContent`, "January 2036")
		b.click(`[data-month-step="1"]`)
		b.waitFor("next month", `document.querySelector('[data-month-title]').textContent`, "February 2036")
		b.click(`[data-month-step="1"]`)
		b.click(`[data-date="2036-03-10"]`)
		b.click(`#outside`)
		b.waitFor("outside keeps selection", `String(!document.querySelector('.schedule-datetime-popup').matches(':popover-open'))+document.querySelector('[name="run_at"]').value`, "true2036-03-10T00:00")
		b.click(`[data-run-at-picker]`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
		b.waitFor("escape leaves schedule dialog open", `String(document.getElementById('editor').open && !document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.getElementById('schedule').reset(); 'reset'`)
		b.click(`[data-run-at-picker]`)
		b.waitFor("form reset restores original time", `document.querySelector('[data-time-column="hour"] [aria-selected="true"]').textContent`, "01")
		b.click(`[data-today]`)
		b.waitFor("today selects current date", `String(document.querySelector('[name="run_at"]').value.slice(0,10)===new Date().getFullYear()+'-'+String(new Date().getMonth()+1).padStart(2,'0')+'-'+String(new Date().getDate()).padStart(2,'0'))`, "true")
		b.waitFor("today preserves time", `document.querySelector('[name="run_at"]').value.slice(11)`, "13:44")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 360, "height": 640, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("popup fits narrow viewport", `(function(){var r=document.querySelector('.schedule-datetime-popup').getBoundingClientRect(); return String(r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight)})()`, "true")
	})
}

func TestBrowserFunctional_SchedulePageDateTimePicker(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "schedule-picker", Name: "Picker"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/schedule" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := Schedule([]models.Project{project}, &project, nil, 0, nil, nil).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/schedule", "schedule-page-picker", func(b *composerFocusCDP) {
		b.waitFor("schedule page ready", `String(typeof openNewScheduledTaskModal==='function')`, "true")
		b.click(`[aria-label="New Scheduled Task"]`)
		b.click(`[data-run-at-picker]`)
		b.waitFor("combined picker opens from schedule page", `String(document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.click(`[data-done]`)
		b.waitFor("local date and time ready to submit", `String(document.querySelector('[data-run-at-picker]').checkValidity() && document.getElementById('new_scheduled_task_modal').open)`, "true")
		b.evaluate(`document.getElementById('new_scheduled_task_modal').close();openNewScheduledTaskModal();'reset'`)
		b.waitFor("new schedule resets selection", `document.querySelector('[data-run-at-picker]').value`, "")
	})
}
