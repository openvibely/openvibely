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
	testScheduleCalendarHeader(t, false, false)
}

func TestBrowserFunctional_ScheduleCalendarPausedHeader(t *testing.T) {
	testScheduleCalendarHeader(t, true, false)
}

func TestBrowserFunctional_ScheduleCalendarMobileSelectionPanel(t *testing.T) {
	testScheduleCalendarHeader(t, false, true)
}

func testScheduleCalendarHeader(t *testing.T, paused, mobile bool) {
	t.Helper()
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
	at := day.AddDate(0, 0, -1).Add(8 * time.Hour)
	tasks = append(tasks, repository.TaskWithSchedule{Task: models.Task{ID: "unaffected", ProjectID: project.ID, Title: "Unaffected run"}, Schedule: &models.Schedule{ID: "unaffected", RunAt: at, NextRun: &at, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}})
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
   var pageHeader=root.querySelector('[data-page-header]');
   var toolbar=root.querySelector('#schedule-selection-toolbar');
   var modifier=/Mac|iPhone|iPad|iPod/.test(navigator.platform)?'Command':'Ctrl';
   check(root.querySelector('[data-calendar-action="pause_all"]').textContent==='Pause all' && root.querySelector('[data-calendar-action="resume_all"]').textContent==='Resume all','project menu uses concise labels');
   var hintCard=root.querySelector('[data-schedule-card][data-has-schedule="true"]');
   check(!hintCard.title.includes(hintCard.dataset.scheduleTitle) && hintCard.title.split('\n').length===2 && hintCard.title===modifier+'+click to select schedule(s)\nRight-click for actions','card hover only shows commands on separate lines');
   check(hintCard.getAttribute('aria-description').includes(modifier+'+click'),'card selection hint available to assistive technology');
   check(root.querySelector('[data-calendar-day]').title==='Click to select or deselect a day\n'+modifier+'+click to select day(s)\nClick and drag across header to select days','day hover uses one line per command');
   check(!root.querySelector('#schedule-day-menu'),'day headers have no custom context menu');
   check(root.querySelector('.drop-zone').title==='Drag empty space to select schedule(s)','calendar space explains box selection');
   check(toolbar.querySelector('[data-calendar-action="clear"]').title==='Clear selection (Esc)','clear hover explains escape shortcut');

   var menu=root.querySelector('#schedule-project-menu');
   function checkHeaderAlignment() {
    var top=pageHeader.getBoundingClientRect().top+parseFloat(getComputedStyle(pageHeader).paddingTop);
    check(Math.abs(pageHeader.querySelector('h2').getBoundingClientRect().top-top)<1,'Schedule title uses standard page header top');
    check(Math.abs(root.querySelector('#schedule-header-actions').getBoundingClientRect().top-top)<1,'New button uses standard page header top');
   }
   checkHeaderAlignment();
   check(pageHeader.contains(toolbar),'selection panel belongs in header');
   check(getComputedStyle(toolbar).display==='none','empty selection hides panel');
   check(!root.querySelector('#schedule-paused-status'),'no redundant paused header status');
   check(pageHeader.contains(menu),'calendar menu belongs beside New');
   check(menu.querySelector('summary svg.h-5.w-5 path'),'standard SVG kebab');
   check(!menu.hidden,'calendar menu stays visible');
   if (JSON.parse(root.querySelector('#schedule-calendar-controls').dataset.state).paused) {
    check(root.querySelector('[data-calendar-action="pause_all"]').hidden,'paused project hides pause action');
    var resume=root.querySelector('[data-calendar-action="resume_all"]');
    check(!resume.hidden && menu.contains(resume),'resume remains in permanent menu');
    menu.open=true;
    resume.click();await tick();
    check(requests[0].action==='resume_all','project resume action works from menu');
    report('pass','');return;
   }
   check(root.querySelector('[data-calendar-action="resume_all"]').hidden,'active project offers pause only');
   var days=Array.from(root.querySelectorAll('[data-calendar-day]'));
   function down(el,options){ el.dispatchEvent(new PointerEvent('pointerdown',Object.assign({bubbles:true,cancelable:true,pointerType:'mouse',button:0,pointerId:8},options||{}))); }
   function up(){window.dispatchEvent(new PointerEvent('pointerup',{pointerId:8}));}
   function count(){return root.querySelectorAll('[data-calendar-day][aria-pressed="true"]').length;}
   var initialHeaderTop=days[1].getBoundingClientRect().top;
   down(days[0]);up();
   check(!toolbar.querySelector('[data-calendar-action="skip"]').hidden && toolbar.querySelector('[data-calendar-action="restore"]').hidden,'untouched day offers only skip');
   down(days[0]);up();
   check(count()===0 && getComputedStyle(toolbar).display==='none','second click deselects day and hides toolbar');
   down(days[0]);up();
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip day(s)','single day label');
   down(days[3],{metaKey:true});up();
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip day(s)','multiple day label');
   down(days[1]);up();
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip remaining run(s)' && toolbar.querySelector('[data-calendar-action="restore"]').textContent==='Unskip skipped run(s)','one partially skipped day has explicit action labels');
   check(days[1].getBoundingClientRect().top===initialHeaderTop,'selection toolbar must not shift headers');down(days[3],{ctrlKey:true});up();check(count()===2,'nonconsecutive header selection');
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip remaining run(s)','partial date selection explains remaining runs');
   check(toolbar.querySelector('[data-calendar-action="restore"]').textContent==='Unskip skipped run(s)','partial date selection explains skipped runs');
   checkHeaderAlignment();
   var panel=toolbar.getBoundingClientRect(), viewport=root.querySelector('#schedule-calendar-viewport').getBoundingClientRect();
   var slot=root.querySelector('#schedule-selection-slot').getBoundingClientRect();
   var title=pageHeader.querySelector('h2').getBoundingClientRect(), actions=root.querySelector('#schedule-header-actions').getBoundingClientRect();
   check(panel.width>0 && panel.left>title.right && panel.right<actions.left,'panel has its own space between title and New');
   check(panel.bottom<=viewport.top,'selection panel never covers calendar cards');
   check(Math.abs((panel.left+panel.right)-(slot.left+slot.right))<2,'panel centered in available header space');
   check(parseFloat(getComputedStyle(toolbar).borderTopWidth)>0,'panel has visible border');
   check(parseFloat(getComputedStyle(toolbar.querySelector('[data-calendar-action="clear"]')).borderTopWidth)===0,'close button has no border');
   if(innerWidth<768) {
    toolbar.querySelector('[data-calendar-action="clear"]').click();
    check(getComputedStyle(toolbar).display==='none','clearing selection hides panel');
    report('pass','');return;
   }
   root.querySelector('#schedule-selection-toolbar [data-calendar-action="skip"]').click();await tick();
   check(requests[0].skips.length===2,'skip must send two exact date windows');
   check(requests[0].skips[0].start_at===Number(days[1].dataset.start),'first date epoch');
   check(requests[0].skips[1].start_at===Number(days[3].dataset.start),'second date epoch');
   root.querySelector('#schedule-timeline-container').scrollTop=0;
   down(days[1]);up();down(days[4],{shiftKey:true});up();check(count()===1 && days[4].getAttribute('aria-pressed')==='true','shift-click behaves like a normal click without selecting a range');
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
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip run(s)' && toolbar.querySelector('[data-calendar-action="pause"]').textContent==='Pause schedule(s)','single occurrence uses singular action labels');
   selectScheduleContextCard(hourCard('s2'));
   check(toolbar.querySelector('[data-calendar-action="skip"]').textContent==='Skip run(s)','hour block acts on multiple runs');
   clearScheduleSelection();
   selectScheduleContextCard(a);
   check(!visible('skip') && visible('restore') && visible('pause') && !visible('resume'),'skipped run only offers unskip and pause');
   check(root.querySelector('#schedule-context-menu [data-calendar-action="restore"]').textContent==='Unskip run(s)','single run uses singular unskip label');
   selectScheduleContextCard(b);
   check(!visible('skip') && !visible('restore') && !visible('pause') && visible('resume'),'paused schedule only offers resume');
   check(root.querySelector('[data-calendar-action="pause_all"]').closest('details'),'pause all belongs in overflow');
   clearScheduleSelection();
   a.dispatchEvent(new MouseEvent('click',{bubbles:true,ctrlKey:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true,ctrlKey:true}));
   a.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:300,clientY:200}));
   check(selectedScheduleCards.size===2,'right click must preserve multi-selection');
   root.querySelector('#schedule-context-menu [data-calendar-action="pause"]').click();await tick();
   check(window._scheduleCalendarUndo.message==='1 schedule(s) paused.','brief feedback counts eligible schedules');
   check(window._scheduleCalendarUndo.expires>Date.now() && window._scheduleCalendarUndo.expires<=Date.now()+6000,'feedback expires');
   check(requests[1].action==='pause' && requests[1].schedule_ids.length===1 && requests[1].schedule_ids[0]==='s0','context pause must affect only eligible schedules');
   clearScheduleSelection();
   var zone=a.closest('.drop-zone');zone.scrollIntoView({block:'center'});
   var ar=a.getBoundingClientRect(),br=b.getBoundingClientRect(),zr=zone.getBoundingClientRect();
   down(zone,{clientX:zr.left+1,clientY:ar.top-1});
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:zr.right-1,clientY:br.bottom+1,cancelable:true}));
   check(selectedScheduleCards.has(a)&&selectedScheduleCards.has(b),'box must select both cards');
   check(getComputedStyle(toolbar).visibility==='visible','header panel remains visible during selection drag');
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:innerWidth+100,clientY:innerHeight+100,cancelable:true}));
   var box=root.ownerDocument.querySelector('.schedule-selection-box').getBoundingClientRect();
   var grid=root.querySelector('#schedule-timeline-container').getBoundingClientRect();
   check(box.left>=grid.left && box.top>=grid.top && box.right<=grid.right && box.bottom<=grid.bottom,'selection box stays inside calendar');
   window.dispatchEvent(new PointerEvent('pointermove',{pointerId:8,clientX:zr.right-1,clientY:br.bottom+1,cancelable:true}));
   up();
   check(getComputedStyle(toolbar).visibility==='visible','selection panel returns after drag');
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
   check(window._scheduleCalendarUndo.message==='1 run(s) unskipped.','unskip feedback uses matching terminology');
   // A later failure gets a fresh notification lifetime even after Undo expired.
   window._scheduleCalendarUndo.expires=Date.now()-1000;
   var nativeTimeout=window.setTimeout, feedbackDelay;
   window.setTimeout=function(callback,delay) {
    if (String(callback).includes("box.classList.add('hidden')")) feedbackDelay=delay;
    return nativeTimeout(callback,delay);
   };
   window.fetch=async function(){return new Response(JSON.stringify({message:'Calendar update failed'}),{status:500,headers:{'Content-Type':'application/json'}});};
   root.querySelector('#schedule-context-menu [data-calendar-action="restore"]').click();await tick();
   var feedback=root.querySelector('#schedule-calendar-feedback');
   check(feedbackDelay===6000,'failed action must receive a fresh six-second lifetime');
   check(!feedback.classList.contains('hidden'),'error must remain visible after expired success');
   check(feedback.querySelector('[data-calendar-message]').textContent==='Calendar update failed','server error is displayed');
   check(feedback.querySelector('[data-calendar-action="undo"]').classList.contains('hidden'),'failed action must not offer Undo');
   window.setTimeout=nativeTimeout;
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
		if err := Schedule([]models.Project{project}, &project, tasks, 0, nil, nil, models.ScheduleCalendarState{Paused: paused, Skips: []models.ScheduleSkip{
			// An elapsed project pause after the daily runs, with no overlapping cards, must not offer Unskip.
			{StartAt: day.Add(-time.Hour).Unix(), EndAt: day.Add(-30 * time.Minute).Unix()},
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
	windowSize := "--window-size=1440,1200"
	if mobile {
		windowSize = "--window-size=500,900"
	}
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", windowSize, "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/schedule?project_id="+project.ID)
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
