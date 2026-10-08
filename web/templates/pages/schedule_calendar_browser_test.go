package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_ScheduleCalendarSelectionActions(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "calendar-selection", Name: "Calendar"}
	day := getStartOfWeek(0).AddDate(0, 0, 1)
	var tasks []repository.TaskWithSchedule
	for i := 0; i < 2; i++ {
		at := day.Add(8 * time.Hour)
		tasks = append(tasks, repository.TaskWithSchedule{Task: models.Task{ID: fmt.Sprintf("t%d", i), ProjectID: project.ID, Title: fmt.Sprintf("Task %d", i)}, Schedule: &models.Schedule{ID: fmt.Sprintf("s%d", i), RunAt: at, NextRun: &at, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: i == 0}})
	}
	for i := 2; i < 5; i++ {
		at := day.Add(12 * time.Hour)
		tasks = append(tasks, repository.TaskWithSchedule{Task: models.Task{ID: fmt.Sprintf("t%d", i), ProjectID: project.ID, Title: fmt.Sprintf("Task %d", i)}, Schedule: &models.Schedule{ID: fmt.Sprintf("s%d", i), RunAt: at, NextRun: &at, RepeatType: models.RepeatMinutes, RepeatInterval: 10, Enabled: true}})
	}
	runner := `<script>
 window.addEventListener('DOMContentLoaded',function() {
  var nativeFetch = window.fetch;
  function report(status,message) { nativeFetch('/browser-result?status='+status+'&message='+encodeURIComponent(message||''),{method:'POST'}); }
  function check(value,message) { if(!value) throw new Error(message); }
  function tick() { return new Promise(function(resolve){setTimeout(resolve,30);}); }
  var requests=[];
  window.fetch=async function(url,opts) { if(url.includes('/schedule/calendar-action')) { requests.push(JSON.parse(opts.body));return new Response(JSON.stringify({undo:{action:'resume',schedule_ids:['s0','s1']}}),{status:200,headers:{'Content-Type':'application/json'}}); } if(url.includes('/reschedule')) { requests.push({action:'drag',ids:opts.body.get('schedule_ids')});return new Response('',{status:200}); } return nativeFetch(url,opts); };
  htmx.ajax=function(){return Promise.resolve();};
  window.addEventListener('error',function(event){report('fail',event.message);});
  (async function(){
   var root=document.getElementById('schedule-content');
   root.querySelector('#schedule-timeline-container').scrollTop=0;
   var days=Array.from(root.querySelectorAll('[data-calendar-day]'));
   function down(el,options){ el.dispatchEvent(new PointerEvent('pointerdown',Object.assign({bubbles:true,cancelable:true,pointerType:'mouse',button:0,pointerId:8},options||{}))); }
   function up(){window.dispatchEvent(new PointerEvent('pointerup',{pointerId:8}));}
   function count(){return root.querySelectorAll('[data-calendar-day][aria-pressed="true"]').length;}
   var initialHeaderTop=days[1].getBoundingClientRect().top;
   down(days[1]);up();check(days[1].getBoundingClientRect().top===initialHeaderTop,'selection toolbar must not shift headers');down(days[3],{ctrlKey:true});up();check(count()===2,'nonconsecutive header selection');
   root.querySelector('#schedule-selection-toolbar [data-calendar-action="skip"]').click();await tick();
   check(requests[0].skips.length===2,'skip must send two exact date windows');
   check(requests[0].skips[0].start_at===Number(days[1].dataset.start),'first date epoch');
   check(requests[0].skips[1].start_at===Number(days[3].dataset.start),'second date epoch');
   root.querySelector('#schedule-timeline-container').scrollTop=0;
   down(days[1]);up();down(days[4],{shiftKey:true});up();check(count()===4,'shift selects contiguous range');
   down(days[1]);
   var headerRect=days[3].getBoundingClientRect();
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:headerRect.left+10,clientY:headerRect.top+10}));
   up();check(count()===3,'header drag selects range');
   document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));check(count()===0,'escape clears dates');
   function hourCard(id) { return root.querySelector('.drop-zone[data-date="'+days[1].dataset.calendarDay+'"][data-hour="12"] [data-schedule-id="'+id+'"]'); }
   ['s2','s3'].forEach(function(id) {
    var partial=hourCard(id);
    check(partial.lastElementChild.textContent==='Partially skipped','partial exclusion label '+id);
    check(partial.dataset.calendarMuted==='false','partial exclusion stays active '+id);
    check(!getComputedStyle(partial.firstElementChild).textDecorationLine.includes('line-through'),'partial exclusion title stays normal '+id);
   });
   check(hourCard('s4').lastElementChild.textContent==='Skipped','adjacent exclusions fully cover block');
   var a=root.querySelector('[data-schedule-id="s0"]'),b=root.querySelector('[data-schedule-id="s1"]');
   check(a.lastElementChild.textContent==='Skipped','skipped run status');
   check(b.lastElementChild.textContent==='Paused','paused schedule status');
   check(getComputedStyle(a.firstElementChild).textDecorationLine.includes('line-through'),'skipped name struck through');
   check(getComputedStyle(a).opacity==='0.5','skipped card muted');
   check(Array.from(root.querySelectorAll('[data-schedule-id="s0"]')).slice(1).every(function(card){return card.dataset.calendarMuted==='false';}),'other dates must stay normal');
   function visible(action) { return !root.querySelector('#schedule-context-menu [data-calendar-action="'+action+'"]').hidden; }
   var normal=Array.from(root.querySelectorAll('[data-schedule-id="s0"]'))[1];
   selectScheduleContextCard(normal);
   check(visible('skip') && visible('pause') && !visible('restore') && !visible('resume'),'normal run only offers skip and pause');
   selectScheduleContextCard(a);
   check(!visible('skip') && visible('restore') && visible('pause') && !visible('resume'),'skipped run only offers unskip and pause');
   check(root.querySelector('#schedule-context-menu [data-calendar-action="restore"]').textContent==='Unskip runs','unskip terminology');
   selectScheduleContextCard(b);
   check(!visible('skip') && !visible('restore') && !visible('pause') && visible('resume'),'paused schedule only offers resume');
   check(root.querySelector('[data-calendar-action="pause_all"]').closest('details'),'pause all belongs in overflow');
   clearScheduleSelection();
   a.dispatchEvent(new MouseEvent('click',{bubbles:true,ctrlKey:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true,ctrlKey:true}));
   a.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:300,clientY:200}));
   check(selectedScheduleCards.size===2,'right click must preserve multi-selection');
   root.querySelector('#schedule-context-menu [data-calendar-action="pause"]').click();await tick();
   check(window._scheduleCalendarUndo.message==='1 schedule paused.','brief feedback counts eligible schedules');
   check(window._scheduleCalendarUndo.expires>Date.now() && window._scheduleCalendarUndo.expires<=Date.now()+6000,'feedback expires');
   check(requests[1].action==='pause' && requests[1].schedule_ids.length===1 && requests[1].schedule_ids[0]==='s0','context pause must affect only eligible schedules');
   clearScheduleSelection();
   var zone=a.closest('.drop-zone');zone.scrollIntoView({block:'center'});
   var ar=a.getBoundingClientRect(),br=b.getBoundingClientRect(),zr=zone.getBoundingClientRect();
   down(zone,{clientX:zr.left+1,clientY:ar.top-1});
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:zr.right-1,clientY:br.bottom+1,cancelable:true}));
   check(selectedScheduleCards.has(a)&&selectedScheduleCards.has(b),'box must select both cards');
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:innerWidth+100,clientY:innerHeight+100,cancelable:true}));
   var box=root.ownerDocument.querySelector('.schedule-selection-box').getBoundingClientRect();
   var grid=root.querySelector('#schedule-timeline-container').getBoundingClientRect();
   check(box.left>=grid.left && box.top>=grid.top && box.right<=grid.right && box.bottom<=grid.bottom,'selection box stays inside calendar');
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:zr.right-1,clientY:br.bottom+1,cancelable:true}));
   up();
   var target=root.querySelector('.drop-zone[data-date="'+zone.dataset.date+'"][data-hour="10"]');
   ar=a.getBoundingClientRect();var tr=target.getBoundingClientRect();
   a.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,cancelable:true,pointerType:'mouse',button:0,pointerId:9,clientX:ar.left+5,clientY:ar.top+5}));
   a.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,cancelable:true,pointerType:'mouse',pointerId:9,clientX:tr.left+10,clientY:tr.top+10}));
   check(a.classList.contains('dragging')&&b.classList.contains('dragging'),'box-selected group must move together');
   a.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,cancelable:true,pointerType:'mouse',pointerId:9,clientX:tr.left+10,clientY:tr.top+10}));await tick();
   check(requests[2].action==='drag'&&requests[2].ids==='s0,s1','group drag must send both IDs');
   selectScheduleContextCard(a);
   root.querySelector('#schedule-context-menu [data-calendar-action="restore"]').click();await tick();
   check(requests[3].action==='restore' && requests[3].skips.length===1 && requests[3].skips[0].schedule_id==='s0','unskip targets only selected skipped runs');
   check(window._scheduleCalendarUndo.message==='1 run unskipped.','unskip feedback uses matching terminology');
   report('pass','');
  })().catch(function(error){report('fail',error.stack||error.message);});
 });
 </script>`
	result := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/schedule" {
			http.NotFound(w, r)
			return
		}
		var out bytes.Buffer
		if err := Schedule([]models.Project{project}, &project, tasks, 0, nil, nil, models.ScheduleCalendarState{Skips: []models.ScheduleSkip{
			{ScheduleID: "s0", StartAt: day.Add(8 * time.Hour).Unix(), EndAt: day.Add(8*time.Hour).Unix() + 1},
			{ScheduleID: "s2", StartAt: day.Add(12*time.Hour + 20*time.Minute).Unix(), EndAt: day.Add(12*time.Hour + 30*time.Minute).Unix()},
			{ScheduleID: "s3", StartAt: day.Add(12 * time.Hour).Unix(), EndAt: day.Add(13 * time.Hour).Unix()},
			{ScheduleID: "s3", StartAt: day.Add(12*time.Hour + 20*time.Minute).Unix(), EndAt: day.Add(12*time.Hour + 30*time.Minute).Unix(), Restored: true},
			{ScheduleID: "s4", StartAt: day.Add(12 * time.Hour).Unix(), EndAt: day.Add(12*time.Hour + 30*time.Minute).Unix()},
			{ScheduleID: "s4", StartAt: day.Add(12*time.Hour + 30*time.Minute).Unix(), EndAt: day.Add(13 * time.Hour).Unix()},
		}}).Render(context.Background(), &out); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(out.String(), "</head>", runner+"</head>", 1)))
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1440,1200", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/schedule?project_id="+project.ID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	var outcome string
	select {
	case outcome = <-result:
	case <-time.After(20 * time.Second):
		outcome = "fail: timed out"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		t.Fatalf("calendar browser test: %s\n%s", outcome, stderr.String())
	}
}
