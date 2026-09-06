package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestTaskAndScheduleCardsUsePointerDragWithGrabCursor(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-card-drag-cursor", Name: "Card Drag Cursor"}
	now := time.Now().Local().Truncate(time.Hour)
	scheduleRunAt := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	tasks := []models.Task{
		{
			ID:           "task-drag-cursor",
			ProjectID:    project.ID,
			Title:        "Drag cursor task",
			Category:     models.CategoryBacklog,
			Status:       models.StatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
			DisplayOrder: 0,
		},
		{
			ID:           "task-active-status-drag",
			ProjectID:    project.ID,
			Title:        "Active queued task",
			Category:     models.CategoryActive,
			Status:       models.StatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
			DisplayOrder: 0,
		},
	}
	scheduledTasks := []repository.TaskWithSchedule{{
		Task: models.Task{
			ID:        "schedule-task-drag-cursor",
			ProjectID: project.ID,
			Title:     "Drag cursor schedule",
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Schedule: &models.Schedule{
			ID:             "schedule-drag-cursor",
			TaskID:         "schedule-task-drag-cursor",
			RunAt:          scheduleRunAt,
			NextRun:        &scheduleRunAt,
			RepeatType:     models.RepeatOnce,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 6000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  async function exerciseOuterAutoScrollDrop(selector, containerSelector, axis, targetSelector, label, pointerId) {
    await waitFor(function() { return document.querySelector(selector) && document.querySelector(containerSelector) && document.querySelector(targetSelector); }, label + ' ready');
    var card = document.querySelector(selector);
    var container = document.querySelector(containerSelector);
    var target = document.querySelector(targetSelector);
    var originalContainerStyle = container.getAttribute('style');
    var childStyles = Array.from(container.children).map(function(child) { return child.getAttribute('style'); });
    if (axis === 'y') {
      var viewportHeight = label === 'task status card' ? 72 : 180;
      container.style.height = viewportHeight + 'px';
      container.style.maxHeight = viewportHeight + 'px';
      container.style.overflowY = 'auto';
      if (containerSelector === '#kanban-board') {
        container.style.display = 'block';
        Array.from(container.children).forEach(function(column) { column.style.minHeight = '240px'; });
      }
    } else {
      container.style.width = '320px';
      container.style.maxWidth = '320px';
      container.style.overflowX = 'auto';
    }
    if (containerSelector === '#schedule-timeline-container') {
      var sourceZone = card.closest('.drop-zone');
      if (!sourceZone) fail(label + ': source zone missing');
      container.scrollTop = Math.max(0, sourceZone.offsetTop - 20);
    } else {
      card.scrollIntoView({block:'center', inline:'center'});
    }
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var cardRect = card.getBoundingClientRect();
    var containerRect = container.getBoundingClientRect();
    var targetRect = target.getBoundingClientRect();
    var targetIsOffscreen = axis === 'y'
      ? (targetRect.bottom <= containerRect.top || targetRect.top >= containerRect.bottom)
      : (targetRect.right <= containerRect.left || targetRect.left >= containerRect.right);
    if (!targetIsOffscreen && containerSelector !== '#schedule-timeline-container') fail(label + ': target must begin outside the scroll viewport');
    var forward = axis === 'y' ? targetRect.top >= containerRect.bottom : targetRect.left >= containerRect.right;
    var edgeX = axis === 'x' ? (forward ? containerRect.right - 4 : containerRect.left + 4) : Math.max(containerRect.left + 8, Math.min(containerRect.right - 8, cardRect.left + 12));
    var edgeY = axis === 'y' ? (forward ? containerRect.bottom - 4 : containerRect.top + 4) : Math.max(containerRect.top + 8, Math.min(containerRect.bottom - 8, cardRect.top + 12));
    var startX = cardRect.left + Math.min(12, cardRect.width / 2);
    var startY = cardRect.top + Math.min(12, cardRect.height / 2);
    if (Math.hypot(edgeX - startX, edgeY - startY) < 4) {
      if (axis === 'y') {
        edgeY = startY + 8 <= containerRect.bottom - 8 ? startY + 8 : startY - 8;
      } else {
        edgeX = startX + 8 <= containerRect.right - 8 ? startX + 8 : startX - 8;
      }
    }
    if (card.hasAttribute('draggable')) fail(label + ': card must use pointer-driven dragging, not native HTML drag');
    if (getComputedStyle(card).cursor !== 'grab') fail(label + ': idle cursor should be grab');
    card.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', button:0, buttons:1, clientX:startX, clientY:startY}));
    card.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', buttons:1, clientX:edgeX, clientY:edgeY}));
    if (!card.classList.contains('dragging')) fail(label + ': pointer movement should mark the card as dragging');
    if (getComputedStyle(card).position !== 'fixed' || getComputedStyle(card).transform === 'none') fail(label + ': real card should visibly move during auto-scroll');
    if (!document.querySelector('[data-pointer-drag-placeholder]')) fail(label + ': source slot should retain its placeholder during auto-scroll');
    var dropX = edgeX;
    var dropY = edgeY;
    if (containerSelector === '#schedule-timeline-container') {
      target.scrollIntoView({block:'center', inline:'center'});
      await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
      targetRect = target.getBoundingClientRect();
      dropX = Math.max(containerRect.left + 8, Math.min(containerRect.right - 8, targetRect.left + targetRect.width / 2));
      dropY = Math.max(containerRect.top + 8, Math.min(containerRect.bottom - 8, targetRect.top + targetRect.height / 2));
      card.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', buttons:1, clientX:dropX, clientY:dropY}));
      await waitFor(function() {
        var hit = document.elementFromPoint(dropX, dropY);
        var zone = hit && hit.closest('.drop-zone');
        return zone === target && target.style.outline !== '';
      }, label + ' target feedback');
    } else {
      await waitFor(function() {
        var hit = document.elementFromPoint(edgeX, edgeY);
        var zone = hit && hit.closest('.category-drop-zone, .task-drop-zone, .drop-zone');
        return zone === target && (target.classList.contains('drag-over') || target.style.outline !== '');
      }, label + ' off-screen target feedback');
    }
    if (getComputedStyle(card).cursor !== 'grabbing') fail(label + ': auto-scroll should preserve grabbing cursor');
    card.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', button:0, buttons:0, clientX:dropX, clientY:dropY}));
    await waitFor(function() { return !card.classList.contains('dragging'); }, label + ' auto-scroll drop cleanup');
    if (card.style.transform || document.querySelector('[data-pointer-drag-placeholder]')) fail(label + ': release should restore card layout');
    if (getComputedStyle(card).cursor !== 'grab') fail(label + ': release should restore grab cursor');
    if (originalContainerStyle === null) container.removeAttribute('style');
    else container.setAttribute('style', originalContainerStyle);
    Array.from(container.children).forEach(function(child, index) {
      if (childStyles[index] === null) child.removeAttribute('style');
      else child.setAttribute('style', childStyles[index]);
    });
    await new Promise(function(resolve) { setTimeout(resolve, 100); });
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    if (location.pathname === '/tasks') {
      if (document.getElementById('selection-counter')) fail('tasks page must not render a selection counter');
      var groupedDrag = new URLSearchParams(location.search).get('grouped_drag');
      if (groupedDrag === 'category' || groupedDrag === 'active') {
        var groupedBacklogTask = document.querySelector('#task-task-drag-cursor');
        var groupedActiveTask = document.querySelector('#task-task-active-status-drag');
        groupedBacklogTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
        groupedActiveTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
        if (!groupedBacklogTask.classList.contains('task-selected') || !groupedActiveTask.classList.contains('task-selected')) fail('grouped drag setup must select both task cards');
        var groupedTarget = groupedDrag === 'active' ? '.task-drop-zone[data-status="running"]' : '.category-drop-zone[data-category="completed"]';
        await exerciseOuterAutoScrollDrop('#task-task-drag-cursor', '#kanban-board', 'y', groupedTarget, 'grouped task ' + groupedDrag + ' cards', groupedDrag === 'active' ? 12 : 8);
        await waitFor(function() {
          return document.querySelectorAll('#kanban-board .task-selected').length === 0;
        }, 'grouped task ' + groupedDrag + ' selection cleanup after authoritative swap');
        location.href = groupedDrag === 'category'
          ? '/tasks?project_id=project-card-drag-cursor&grouped_drag=active'
          : '/tasks?project_id=project-card-drag-cursor&drag_only=1';
        return;
      }
      if (new URLSearchParams(location.search).get('drag_only') === '1') {
        var toggleTask = document.querySelector('#task-task-drag-cursor');
        toggleTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
        if (!toggleTask.classList.contains('task-selected')) fail('tasks page modifier click must still select a card');
        toggleTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
        if (toggleTask.classList.contains('task-selected')) fail('tasks page second modifier click must clear card selection');
        await exerciseOuterAutoScrollDrop('#task-task-drag-cursor', '#kanban-board', 'y', '.category-drop-zone[data-category="completed"]', 'task category card', 9);
        await exerciseOuterAutoScrollDrop('#task-task-active-status-drag', '#kanban-board', 'y', '.task-drop-zone[data-status="running"]', 'task status card', 10);
        location.href = '/schedule?project_id=project-card-drag-cursor';
        return;
      }
      var selectedTask = document.querySelector('#task-task-drag-cursor');
      var selectedActiveTask = document.querySelector('#task-task-active-status-drag');
      selectedTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      selectedActiveTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      if (!selectedTask.classList.contains('task-selected') || !selectedActiveTask.classList.contains('task-selected')) fail('tasks page modifier clicks must select both cards');
      var taskMenuTrigger = selectedActiveTask.querySelector('[data-kanban-menu-trigger]');
      taskMenuTrigger.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, cancelable:true}));
      taskMenuTrigger.focus();
      taskMenuTrigger.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, detail:1}));
	      if (taskMenuTrigger.getAttribute('aria-expanded') !== 'true') fail('task menu must expose its open state');
	      if (selectedActiveTask.querySelector('[data-task-card-merge-options]')) fail('task menu must not hydrate merge options after opening');
	      if (document.querySelector('#kanban-board').getAttribute('data-open-kanban-menu-key') !== 'task-task-active-status-drag') fail('task menu open key was not recorded before refresh');
	      await waitFor(function() { return taskMenuTrigger.closest('[data-kanban-menu-key]').getAttribute('data-kanban-menu-positioning') !== 'true'; }, 'task menu stable reveal before option focus');
	      var focusedTaskOption = Array.from(selectedActiveTask.querySelectorAll('[data-kanban-menu-content] a, [data-kanban-menu-content] button')).find(function(option) { return option.textContent.trim() === 'Edit'; });
      if (!focusedTaskOption) fail('task menu must render the pre-refresh Edit option');
      focusedTaskOption.focus();
      if (document.activeElement !== focusedTaskOption) fail('task menu option did not receive focus before refresh');
      await htmx.ajax('GET', '/refresh-kanban?state=running', {target:'#kanban-board', swap:'outerHTML'});
      selectedTask = document.querySelector('#task-task-drag-cursor');
      selectedActiveTask = document.querySelector('#task-task-active-status-drag');
      taskMenuTrigger = selectedActiveTask.querySelector('[data-kanban-menu-trigger]');
      if (!selectedTask.classList.contains('task-selected') || !selectedActiveTask.classList.contains('task-selected')) fail('authoritative refresh cleared multi-selection');
      if (document.querySelector('#kanban-board').getAttribute('data-open-kanban-menu-key') !== 'task-task-active-status-drag') fail('task menu open key was not restored: key=' + document.querySelector('#kanban-board').getAttribute('data-open-kanban-menu-key'));
	      await waitFor(function() { return document.activeElement === taskMenuTrigger && taskMenuTrigger.getAttribute('aria-expanded') === 'true'; }, 'removed task option fallback to menu trigger after settle (active=' + (document.activeElement && document.activeElement.outerHTML) + ', connected=' + !!(document.activeElement && document.activeElement.isConnected) + ', activeCardStatus=' + (document.activeElement && document.activeElement.closest('[data-task-status]') && document.activeElement.closest('[data-task-status]').getAttribute('data-task-status')) + ', expanded=' + taskMenuTrigger.getAttribute('aria-expanded') + ', open=' + taskMenuTrigger.closest('[data-kanban-menu-key]').hasAttribute('data-kanban-menu-open') + ')');      if (!selectedActiveTask.querySelector('[data-kanban-menu-content]').textContent.includes('Cancel')) fail('task menu did not retain authoritative running options');
      var columnMenu = document.querySelector('[data-kanban-menu-key="column-backlog"]');
      var columnMenuTrigger = columnMenu.querySelector('[data-kanban-menu-trigger]');
      columnMenuTrigger.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, cancelable:true}));
      columnMenuTrigger.focus();
      columnMenuTrigger.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, detail:1}));
      await waitFor(function() { return columnMenu.getAttribute('data-kanban-menu-positioning') !== 'true'; }, 'dropzone menu stable reveal before option focus');
      var focusedColumnOption = columnMenu.querySelector('[data-kanban-menu-content] button[hx-post]');
      focusedColumnOption.focus();
      if (document.activeElement !== focusedColumnOption) fail('dropzone menu option is not keyboard focusable before refresh');
      var focusedColumnOptionKey = focusedColumnOption.getAttribute('hx-post');
      await htmx.ajax('GET', '/refresh-kanban?state=running', {target:'#kanban-board', swap:'outerHTML'});
      columnMenu = document.querySelector('[data-kanban-menu-key="column-backlog"]');
      columnMenuTrigger = columnMenu.querySelector('[data-kanban-menu-trigger]');
      focusedColumnOption = Array.from(columnMenu.querySelectorAll('[data-kanban-menu-content] button[hx-post]')).find(function(option) { return option.getAttribute('hx-post') === focusedColumnOptionKey; });
      if (!focusedColumnOption) fail('surviving dropzone option was removed by authoritative refresh');
      await waitFor(function() { return document.activeElement === focusedColumnOption && columnMenuTrigger.getAttribute('aria-expanded') === 'true'; }, 'surviving dropzone menu option focus restoration after settle (active=' + (document.activeElement && document.activeElement.outerHTML) + ', expanded=' + columnMenuTrigger.getAttribute('aria-expanded') + ', open=' + columnMenu.hasAttribute('data-kanban-menu-open') + ')');

      columnMenuTrigger.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, cancelable:true}));
      columnMenuTrigger.focus();
      columnMenuTrigger.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, detail:1}));
      if (columnMenuTrigger.getAttribute('aria-expanded') !== 'false' || columnMenu.hasAttribute('data-kanban-menu-open')) fail('trigger dismissal must close the dropzone menu');
      await htmx.ajax('GET', '/refresh-kanban?state=running', {target:'#kanban-board', swap:'outerHTML'});
      columnMenu = document.querySelector('[data-kanban-menu-key="column-backlog"]');
      columnMenuTrigger = columnMenu.querySelector('[data-kanban-menu-trigger]');
      if (columnMenuTrigger.getAttribute('aria-expanded') !== 'false' || columnMenu.hasAttribute('data-kanban-menu-open')) fail('trigger-dismissed menu reopened after refresh');

      columnMenuTrigger.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, cancelable:true}));
      columnMenuTrigger.focus();
      columnMenuTrigger.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, detail:1}));
      focusedColumnOption = columnMenu.querySelector('[data-kanban-menu-content] button[hx-post]');
      focusedColumnOption.focus();
      focusedColumnOption.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true, cancelable:true}));
      if (document.activeElement !== columnMenuTrigger || columnMenuTrigger.getAttribute('aria-expanded') !== 'false' || columnMenu.hasAttribute('data-kanban-menu-open')) fail('Escape must close the menu and restore trigger focus');
      await htmx.ajax('GET', '/refresh-kanban?state=running', {target:'#kanban-board', swap:'outerHTML'});
      columnMenu = document.querySelector('[data-kanban-menu-key="column-backlog"]');
      columnMenuTrigger = columnMenu.querySelector('[data-kanban-menu-trigger]');
      if (columnMenuTrigger.getAttribute('aria-expanded') !== 'false' || columnMenu.hasAttribute('data-kanban-menu-open')) fail('Escape-dismissed menu reopened after refresh');

      columnMenuTrigger.focus();
      var outside = document.createElement('button');
      document.body.appendChild(outside);
      outside.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, pointerId:18, pointerType:'mouse', button:0}));
      outside.focus();
      await new Promise(function(resolve) { setTimeout(resolve, 0); });
      if (columnMenuTrigger.getAttribute('aria-expanded') !== 'false') fail('intentional outside focus must close the menu accessibly');
      selectedTask = document.querySelector('#task-task-drag-cursor');
      selectedActiveTask = document.querySelector('#task-task-active-status-drag');
      selectedTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      selectedActiveTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      if (selectedTask.classList.contains('task-selected') || selectedActiveTask.classList.contains('task-selected')) fail('modifier click must still clear restored selection');
      selectedActiveTask = document.querySelector('#task-task-active-status-drag');
      taskMenuTrigger = selectedActiveTask.querySelector('[data-kanban-menu-trigger]');
      taskMenuTrigger.focus();
      await htmx.ajax('GET', '/refresh-kanban?state=removed', {target:'#kanban-board', swap:'outerHTML'});
      if (document.querySelector('#task-task-active-status-drag')) fail('removed menu invoker survived authoritative refresh');
      if (document.activeElement && document.activeElement.closest && document.activeElement.closest('[data-kanban-menu-key="task-task-active-status-drag"]')) fail('removed invoker retained stale menu focus');

      selectedTask = document.querySelector('#task-task-drag-cursor');
      selectedTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      if (!selectedTask.classList.contains('task-selected')) fail('navigation setup must select the surviving task');
      taskMenuTrigger = selectedTask.querySelector('[data-kanban-menu-trigger]');
      taskMenuTrigger.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, cancelable:true}));
      taskMenuTrigger.focus();
      taskMenuTrigger.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, detail:1}));
      var editTask = Array.from(selectedTask.querySelectorAll('[data-kanban-menu-content] a')).find(function(option) { return option.textContent.trim() === 'Edit'; });
      htmx.process(selectedTask);
      editTask.click();
      await waitFor(function() { return document.querySelector('[data-browser-task-detail]'); }, 'task menu navigation');
      if (taskMenuTrigger.getAttribute('aria-expanded') !== 'false') fail('task menu navigation did not clear open state before replacement');
      history.back();
      await waitFor(function() { return document.querySelector('#kanban-board #task-task-drag-cursor'); }, 'Tasks history restoration');
      if (document.querySelectorAll('#kanban-board .task-selected').length !== 0) fail('history restoration revived cached task selection');
      if (document.querySelector('#kanban-board [data-kanban-menu-open]') || document.querySelector('#kanban-board').hasAttribute('data-open-kanban-menu-key')) fail('history restoration revived cached Kanban menu state');
      var restoredTask = document.querySelector('#task-task-drag-cursor');
      restoredTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      if (!restoredTask.classList.contains('task-selected')) fail('history-restored board retained stale internal selection state');
      restoredTask.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:true}));
      location.href = '/tasks?project_id=project-card-drag-cursor&grouped_drag=category';
      return;
    }
    if (location.pathname === '/schedule') {
      if (document.getElementById('selection-counter')) fail('schedule page must not render a selection counter');
      var scheduleCard = document.querySelector('[data-schedule-id="schedule-drag-cursor"]');
      var sourceZone = scheduleCard && scheduleCard.closest('.drop-zone');
      if (!sourceZone) fail('schedule off-screen drop: source zone missing');
      var sourceHour = Number(sourceZone.dataset.hour);
      var targetHour = sourceHour <= 17 ? sourceHour + 6 : sourceHour - 6;
      var scheduleTargetSelector = '.drop-zone[data-date="' + sourceZone.dataset.date + '"][data-hour="' + targetHour + '"]';
      await exerciseOuterAutoScrollDrop('[data-schedule-id="schedule-drag-cursor"]', '#schedule-timeline-container', 'y', scheduleTargetSelector, 'schedule card vertical', 11);
      await report('pass', '');
      return;
    }
    fail('unexpected fixture path ' + location.pathname);
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 4)
	var requestMu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		requestMu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/tasks":
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/refresh-kanban":
			refreshedTasks := append([]models.Task(nil), tasks...)
			if r.URL.Query().Get("state") == "removed" {
				refreshedTasks = refreshedTasks[:1]
			} else {
				refreshedTasks[1].Status = models.StatusRunning
			}
			var out bytes.Buffer
			if err := components.KanbanBoard(refreshedTasks, project.ID, "created_desc", "completed_desc", nil, nil).Render(context.Background(), &out); err != nil {
				t.Fatalf("render refreshed kanban: %v", err)
			}
			_, _ = w.Write(out.Bytes())
		case "/tasks/task-drag-cursor":
			if r.Header.Get("HX-Request") != "true" {
				t.Fatalf("expected task menu navigation to use HTMX")
			}
			_, _ = w.Write([]byte(`<div data-browser-task-detail>Task detail</div>`))
		case "/schedule":
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(`<div id="schedule-content" data-project-id="project-card-drag-cursor"></div>`))
				return
			}
			var out bytes.Buffer
			if err := Schedule([]models.Project{project}, &project, scheduledTasks, 0, nil, nil).Render(context.Background(), &out); err != nil {
				t.Fatalf("render Schedule page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/batch-category":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected grouped task category move to use PATCH, got %s", r.Method)
			}
			var out bytes.Buffer
			if err := components.KanbanBoard(tasks, project.ID, "created_desc", "completed_desc", nil, nil).Render(context.Background(), &out); err != nil {
				t.Fatalf("render grouped task drop response: %v", err)
			}
			_, _ = w.Write(out.Bytes())
		case "/tasks/task-drag-cursor/category":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected task category move to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-active-status-drag/status":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected Active lane status move to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-active-status-drag/reorder":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected task reorder to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/schedules/schedule-drag-cursor/reschedule":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected schedule reschedule to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/schedule-task-drag-cursor":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "card-drag-cursor-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900",
		"--user-data-dir="+filepath.Join(t.TempDir(), "card-drag-cursor-browser-profile"),
		server.URL+"/tasks?project_id="+project.ID,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(20 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	requestMu.Lock()
	requestList := strings.Join(requests, "\n")
	requestMu.Unlock()
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Card drag cursor browser regression failed: %s\nRequests:\n%s\nChrome:\n%s", outcome, requestList, strings.TrimSpace(string(stderr)))
	}
	for _, want := range []string{
		"PATCH /tasks/batch-category",
		"PATCH /tasks/task-drag-cursor/category",
		"PATCH /tasks/task-active-status-drag/status",
		"PATCH /schedules/schedule-drag-cursor/reschedule",
	} {
		if !strings.Contains(requestList, want) {
			t.Fatalf("browser drag should preserve drop behavior; missing request %q in:\n%s", want, requestList)
		}
	}
	if strings.Contains(requestList, "PATCH /tasks/task-active-status-drag/reorder") {
		t.Fatalf("Active status-lane drag must not be routed as a reorder:\n%s", requestList)
	}
}

func TestScheduleCardsSupportModifierMultiSelectGroupedPointerDragAndRollback(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	project := models.Project{ID: "project-schedule-multi-drag", Name: "Schedule multi drag"}
	start := getStartOfWeek(0).AddDate(0, 0, 1)
	makeScheduled := func(id string, hour int, enabled bool) repository.TaskWithSchedule {
		runAt := time.Date(start.Year(), start.Month(), start.Day(), hour, 0, 0, 0, time.Local)
		return repository.TaskWithSchedule{Task: models.Task{ID: "task-" + id, ProjectID: project.ID, Title: id, Category: models.CategoryScheduled, Status: models.StatusPending, CreatedAt: start, UpdatedAt: start}, Schedule: &models.Schedule{ID: id, TaskID: "task-" + id, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: enabled}}
	}
	scheduled := []repository.TaskWithSchedule{makeScheduled("schedule-multi-a", 8, true), makeScheduled("schedule-multi-b", 8, false), makeScheduled("schedule-single-c", 12, true)}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) { var started = performance.now(); return new Promise(function(resolve, reject) { (function poll() { try { if (check()) return resolve(); } catch (error) { return reject(error); } if (performance.now() - started > 5000) return reject(new Error('timed out waiting for ' + label)); setTimeout(poll, 10); })(); }); }
  function click(card, ctrl) { card.dispatchEvent(new MouseEvent('click', {bubbles:true, cancelable:true, ctrlKey:ctrl})); }
  function point(card) { var r = card.getBoundingClientRect(); return {x:r.left + Math.min(8, r.width / 2), y:r.top + Math.min(8, r.height / 2)}; }
  async function drag(card, target, pointerId) {
    var start = point(card), rect = target.getBoundingClientRect(), x = rect.left + rect.width / 2, y = rect.top + rect.height / 2;
    card.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true,cancelable:true,pointerId:pointerId,pointerType:'mouse',button:0,buttons:1,clientX:start.x,clientY:start.y}));
    card.dispatchEvent(new PointerEvent('pointermove', {bubbles:true,cancelable:true,pointerId:pointerId,pointerType:'mouse',buttons:1,clientX:x,clientY:y}));
    return {x:x,y:y};
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    var first = document.querySelector('[data-schedule-id="schedule-multi-a"]');
    var second = document.querySelector('[data-schedule-id="schedule-multi-b"]');
    var single = document.querySelector('[data-schedule-id="schedule-single-c"]');
    if (!first || !second || !single) fail('expected all schedule cards');
    if (!second.textContent.includes('paused')) fail('second schedule must render as paused');
    if (getComputedStyle(second).cursor === 'not-allowed') fail('paused schedule must expose draggable cursor');
    if (document.getElementById('selection-counter')) fail('selection counter must not render');
    var singleHeight = single.getBoundingClientRect().height;
    click(single, true);
    if (!single.classList.contains('schedule-selected')) fail('modifier click must select one schedule');
    if (single.getBoundingClientRect().height !== singleHeight) fail('schedule selection must not change card height');
    click(single, true);
    if (single.classList.contains('schedule-selected')) fail('second modifier click must clear selection');

    var firstHeight = first.getBoundingClientRect().height, secondHeight = second.getBoundingClientRect().height;
    click(first, true); click(second, true);
    if (!first.classList.contains('schedule-selected') || !second.classList.contains('schedule-selected')) fail('modifier clicks must select both schedules');
    if (first.getBoundingClientRect().height !== firstHeight || second.getBoundingClientRect().height !== secondHeight) fail('multi-selection must not change schedule card heights');
    var firstStyle = getComputedStyle(first), secondStyle = getComputedStyle(second);
    if (firstStyle.outlineStyle !== 'none' || secondStyle.outlineStyle !== 'none') fail('schedule selection must not use a floating outline');
    if (firstStyle.boxShadow !== 'none' || secondStyle.boxShadow !== 'none') fail('schedule selection must not use an external shadow ring');
    var firstSelectionStroke = getComputedStyle(first, '::after');
    if (firstSelectionStroke.borderTopWidth === '0px' || firstSelectionStroke.borderRightWidth === '0px' || firstSelectionStroke.borderBottomWidth === '0px' || firstSelectionStroke.borderLeftWidth === '0px') fail('schedule selection must draw an inset card border on every side');
    if (firstSelectionStroke.left !== '-2px') fail('schedule selection must cover the card own left accent border: left=' + firstSelectionStroke.left);
    var firstRect = first.getBoundingClientRect(), secondRect = second.getBoundingClientRect();
    if (firstRect.bottom > secondRect.top) fail('selected schedules in one time block must not overlap');
    var source = first.closest('.drop-zone');
    var targetHour = Number(source.dataset.hour) + 4;
    var target = document.querySelector('.drop-zone[data-date="' + source.dataset.date + '"][data-hour="' + targetHour + '"]');
    var drop = await drag(second, target, 31);
    if (!first.classList.contains('dragging') || !second.classList.contains('dragging')) fail('every selected schedule must enter dragging state');
    if (getComputedStyle(first).position !== 'fixed' || getComputedStyle(second).position !== 'fixed') fail('every selected schedule card must visibly move');
    if (first.style.transform === '' || first.style.transform !== second.style.transform) fail('selected schedules must move by the same pointer delta');
    if (document.querySelectorAll('[data-pointer-drag-placeholder]').length !== 2) fail('each moved card needs a source placeholder');
    second.dispatchEvent(new PointerEvent('pointerup', {bubbles:true,cancelable:true,pointerId:31,pointerType:'mouse',button:0,buttons:0,clientX:drop.x,clientY:drop.y}));
    await waitFor(function() { return !first.classList.contains('dragging') && !second.classList.contains('dragging'); }, 'failed grouped drag rollback');
    if (first.style.transform || second.style.transform || document.querySelector('[data-pointer-drag-placeholder]')) fail('failed grouped drag must restore every card');
    if (first.classList.contains('schedule-selected') || second.classList.contains('schedule-selected')) fail('drag completion must clear schedule selection');
    await new Promise(function(resolve) { setTimeout(resolve, 20); });

    click(first, true); click(second, true);
    drop = await drag(second, target, 32);
    second.dispatchEvent(new PointerEvent('pointerup', {bubbles:true,cancelable:true,pointerId:32,pointerType:'mouse',button:0,buttons:0,clientX:drop.x,clientY:drop.y}));
    await waitFor(function() { return document.querySelectorAll('[data-schedule-id]').length === 3; }, 'successful grouped refresh');
    await new Promise(function(resolve) { setTimeout(resolve, 20); });
    var refreshed = document.querySelector('[data-schedule-id="schedule-single-c"]');
    click(refreshed, true);
    if (!refreshed.classList.contains('schedule-selected')) fail('selection must reinitialize after authoritative refresh');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	var mu sync.Mutex
	var mutations []url.Values
	groupAttempts := 0
	result := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/schedule":
			if r.Header.Get("HX-Request") == "true" {
				var fragment bytes.Buffer
				if err := ScheduleContent(&project, scheduled, 0, nil, nil).Render(context.Background(), &fragment); err != nil {
					t.Errorf("render refreshed schedule content: %v", err)
				}
				_, _ = w.Write(fragment.Bytes())
				return
			}
			var out bytes.Buffer
			if err := Schedule([]models.Project{project}, &project, scheduled, 0, nil, nil).Render(context.Background(), &out); err != nil {
				t.Fatalf("render schedule: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/schedules/schedule-multi-b/reschedule":
			if r.Method != http.MethodPatch {
				t.Errorf("expected PATCH, got %s", r.Method)
			}
			if r.URL.Query().Get("project_id") != project.ID {
				t.Errorf("grouped mutation lost project scope: %s", r.URL.RawQuery)
			}
			_ = r.ParseMultipartForm(1 << 20)
			mu.Lock()
			mutations = append(mutations, r.Form)
			groupAttempts++
			attempt := groupAttempts
			mu.Unlock()
			if attempt == 1 {
				http.Error(w, "forced grouped failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/browser-result":
			result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stderrPath := filepath.Join(t.TempDir(), "schedule-multi-drag.stderr")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "schedule-multi-profile"), server.URL+"/schedule?project_id="+project.ID)
	cmd.Stderr = stderr
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-result:
	case <-time.After(20 * time.Second):
		outcome = "fail:timed out"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		browserErr, _ := os.ReadFile(stderrPath)
		t.Fatalf("schedule multi-select browser regression failed: %s\n%s", outcome, browserErr)
	}
	mu.Lock()
	got := append([]url.Values(nil), mutations...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected failed and successful grouped mutations, got %d", len(got))
	}
	for i, form := range got {
		if form.Get("schedule_ids") != "schedule-multi-a,schedule-multi-b" {
			t.Errorf("mutation %d schedule_ids=%q", i, form.Get("schedule_ids"))
		}
		if form.Get("source_date") == "" || form.Get("source_hour") != "8" || form.Get("hour") != "12" {
			t.Errorf("mutation %d lost source/target slot: %v", i, form)
		}
	}
}
