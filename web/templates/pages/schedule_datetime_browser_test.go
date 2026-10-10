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
		b.waitFor("readable initial value", `document.querySelector('[data-run-at-picker]').value`, "02/29/2036, 01:44 PM")
		b.click(`[data-run-at-picker]`)
		b.waitFor("picker opens inside modal", `String(document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.dispatchEvent(new CustomEvent('htmx:beforeSwap',{detail:{target:document.getElementById('outside')}})); 'refreshed'`)
		b.waitFor("unrelated refresh keeps picker open", `String(document.querySelector('.schedule-datetime-popup').matches(':popover-open'))`, "true")
		b.waitFor("leap day selected", `document.querySelector('[data-date="2036-02-29"]').getAttribute('aria-pressed')`, "true")
		b.waitFor("existing time restored", `document.querySelector('[data-time-column="hour"] [aria-selected="true"]').textContent + ':' + document.querySelector('[data-time-column="minute"] [aria-selected="true"]').textContent + document.querySelector('[data-time-column="period"] [aria-selected="true"]').textContent`, "01:44PM")
		b.waitFor("no internal dividers", `String(getComputedStyle(document.querySelector('.schedule-datetime-time')).borderLeftWidth==='0px' && !document.querySelector('[data-done]'))`, "true")
		for _, kind := range []string{"hour", "minute"} {
			b.evaluate(fmt.Sprintf(`var col=document.querySelector('[data-time-column="%s"]'); col.scrollTop=15; 'scrolled'`, kind))
			b.waitFor(kind+" wraps upward", fmt.Sprintf(`(function(){var c=document.querySelector('[data-time-column="%s"]'),span=c.children.length/5*30;return String(Math.abs(c.scrollTop-(span*2+15))<1)})()`, kind), "true")
			b.evaluate(fmt.Sprintf(`var col=document.querySelector('[data-time-column="%s"]'); col.scrollTop=col.children.length/5*30*3+15; 'scrolled'`, kind))
			b.waitFor(kind+" wraps downward", fmt.Sprintf(`(function(){var c=document.querySelector('[data-time-column="%s"]'),span=c.children.length/5*30;return String(Math.abs(c.scrollTop-(span*2+15))<1)})()`, kind), "true")
		}
		b.waitFor("browsing preserves selection", `document.querySelector('[name="run_at"]').value`, "2036-02-29T13:44")
		b.evaluate(`document.querySelector('[data-time-column="minute"]').focus(); 'focused'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "End"}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowDown"}, nil)
		b.waitFor("minutes wrap from 59 to 00", `document.querySelector('[name="run_at"]').value`, "2036-02-29T13:00")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowUp"}, nil)
		b.waitFor("minutes wrap from 00 to 59", `document.querySelector('[name="run_at"]').value`, "2036-02-29T13:59")
		b.evaluate(`document.querySelector('[data-time-column="hour"]').focus(); 'focused'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "End"}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowDown"}, nil)
		b.waitFor("hours wrap from 12 to 01", `document.querySelector('[name="run_at"]').value`, "2036-02-29T13:59")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowUp"}, nil)
		b.waitFor("hours wrap from 01 to 12", `document.querySelector('[name="run_at"]').value`, "2036-02-29T12:59")
		b.click(`[data-time-column="hour"] [data-cycle="2"][data-value="12"]`)
		b.click(`[data-time-column="period"] [data-value="0"]`)
		b.evaluate(`document.querySelector('[data-time-column="minute"]').focus(); 'focused'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Home"}, nil)
		b.click(`#outside`)
		b.waitFor("midnight serialized in local format", `new FormData(document.getElementById('schedule')).get('run_at')`, "2036-02-29T00:00")
		b.waitFor("outside closes picker only", `String(!document.querySelector('.schedule-datetime-popup').matches(':popover-open') && document.getElementById('editor').open)`, "true")
		b.click(`[data-run-at-picker]`)
		b.evaluate(`document.querySelectorAll('[data-month-step], [data-today]').forEach(button=>button.replaceWith(button.cloneNode(true))); 'controls refreshed'`)
		b.click(`[data-month-step="-1"]`)
		b.waitFor("previous month retains focus", `String(document.activeElement.matches('[data-month-step="-1"]'))`, "true")
		b.waitFor("previous month", `document.querySelector('[data-month-title]').textContent`, "January 2036")
		b.click(`[data-month-step="1"]`)
		b.waitFor("next month retains focus", `String(document.activeElement.matches('[data-month-step="1"]'))`, "true")
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
		b.waitFor("today retains focus", `String(document.activeElement.matches('[data-today]'))`, "true")
		b.waitFor("today navigates to current month", `String(document.querySelector('[data-month-title]').textContent===new Date().toLocaleDateString(undefined,{month:'long',year:'numeric'}))`, "true")
		b.waitFor("today preserves time", `document.querySelector('[name="run_at"]').value.slice(11)`, "13:44")
		b.click(`#outside`)
		b.call("Emulation.setTimezoneOverride", map[string]any{"timezoneId": "America/New_York"}, nil)
		b.evaluate(`document.querySelector('[name="run_at"]').value='2036-03-08T02:30'; window.refreshScheduleDateTimes(document); 'configured'`)
		b.click(`[data-run-at-picker]`)
		b.click(`[data-date="2036-03-09"]`)
		b.waitFor("DST date normalizes stored time", `document.querySelector('[name="run_at"]').value`, "2036-03-09T03:30")
		b.waitFor("DST date synchronizes time columns", `document.querySelector('[data-time-column="hour"] [aria-selected="true"]').textContent + ':' + document.querySelector('[data-time-column="minute"] [aria-selected="true"]').textContent + document.querySelector('[data-time-column="period"] [aria-selected="true"]').textContent`, "03:30AM")
		b.waitFor("DST date synchronizes displayed value", `document.querySelector('[data-run-at-picker]').value`, "03/09/2036, 03:30 AM")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 360, "height": 640, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("popup fits narrow viewport", `(function(){var r=document.querySelector('.schedule-datetime-popup').getBoundingClientRect(); return String(r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight)})()`, "true")
		for i := 0; i < 6; i++ {
			b.click(`[data-month-step="1"]`)
		}
		b.waitFor("long month displayed", `document.querySelector('[data-month-title]').textContent`, "September 2036")
		for _, width := range []int{320, 360, 400, 450, 800} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 640, "deviceScaleFactor": 1, "mobile": false}, nil)
			b.waitFor(fmt.Sprintf("header stays inside calendar at %dpx", width), `(function(){const header=document.querySelector('.schedule-datetime-month'),hour=document.querySelector('[data-time-column="hour"]').getBoundingClientRect(),weekdays=document.querySelector('.schedule-datetime-weekdays').getBoundingClientRect();return String(Array.from(header.children).every(el=>{const r=el.getBoundingClientRect();return r.right<=hour.left && r.bottom<=weekdays.top && r.left>=header.getBoundingClientRect().left;}));})()`, "true")
		}

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
		b.click(`#sched-task-title-input`)
		b.waitFor("local date and time ready to submit", `String(document.querySelector('[data-run-at-picker]').checkValidity() && document.getElementById('new_scheduled_task_modal').open)`, "true")
		b.evaluate(`document.getElementById('new_scheduled_task_modal').close();openNewScheduledTaskModal();'reset'`)
		b.waitFor("new schedule defaults to future minute", `String(new Date(document.querySelector('[name="run_at"]').value).getTime()>Date.now() && !!document.querySelector('.schedule-datetime-icon'))`, "true")
	})
}
