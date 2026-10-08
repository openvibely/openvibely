/* Column interactions share the board's existing selection and move pipeline. */
(function () {
    'use strict';
    const filters = new Map(), results = new Map();
    let pendingDelete = null;
    const column = name => document.querySelector('#kanban-board [data-kanban-category="' + name + '"]');
    const key = col => col.dataset.projectId + ':' + col.dataset.kanbanCategory;
    const cards = col => Array.from(col.querySelectorAll('.card[data-task-id]'));
    const selection = () => window.kanbanSelection || new Set();
    function matches(data, filter) {
        if (filter.status && data.taskStatus !== filter.status.value) return false;
        if (filter.priority && data.taskPriority !== filter.priority.value) return false;
        const merge = filter.merge && filter.merge.value;
        if (merge === 'none') return !data.taskBranch;
        if (merge === 'unmerged') return !!data.taskBranch && data.taskMerge !== 'merged';
        return !merge || data.taskMerge === merge;
    }
    function scope(col) {
        const visible = cards(col).filter(card => !card.hidden);
        const selected = visible.filter(card => selection().has(card.dataset.taskId));
        return selected.length ? selected : visible;
    }
    function textElement(tag, text) {
        const el = document.createElement(tag); el.textContent = text; return el;
    }
    function currentColumn(batchKey) {
        return Array.from(document.querySelectorAll('#kanban-board [data-kanban-category]')).find(col => key(col) === batchKey);
    }
    function renderProgress(col) {
        if (!col) return;
        const host = col.querySelector('[data-kanban-progress]'), batch = results.get(key(col));
        if (!host || !batch) return;
        let view = host.kanbanProgressView;
        if (!view || view.batch !== batch) {
            const status = textElement('div', '');
            const progress = document.createElement('progress');
            const button = textElement('button', '');
            button.className = 'btn btn-xs btn-ghost';
            const details = document.createElement('details');
            details.append(textElement('summary', 'Details'));
            details.open = !!batch.detailsOpen;
            details.ontoggle = () => { batch.detailsOpen = details.open; };
            button.onclick = () => {
                if (batch.running) { batch.stop = true; button.disabled = true; }
                else { results.delete(key(col)); host.replaceChildren(); host.kanbanProgressView = null; }
            };
            host.replaceChildren(status, progress, button, details);
            view = host.kanbanProgressView = { batch, status, progress, button, details, lineCount: 0 };
        }
        view.status.textContent = (batch.stop && !batch.running ? 'Stopped · ' : '') + batch.label + ': ' + batch.done + '/' + batch.total + ' · ' + batch.success + ' succeeded · ' + batch.skipped + ' skipped · ' + batch.failed + ' failed';
        view.progress.hidden = !batch.running;
        view.progress.max = batch.total; view.progress.value = batch.done;
        view.button.textContent = batch.running ? 'Stop' : '×';
        view.button.disabled = batch.running && batch.stop;
        view.button.setAttribute('aria-label', batch.running ? 'Stop batch' : 'Dismiss results');
        view.details.hidden = !batch.lines.length;
        while (view.lineCount < batch.lines.length) {
            view.details.append(textElement('div', batch.lines[view.lineCount++]));
        }
    }
    function refresh() {
        document.querySelectorAll('#kanban-board [data-kanban-category]').forEach(col => {
            const filter = filters.get(key(col)) || {};
            const all = cards(col);
            all.forEach(card => {
                card.hidden = col.dataset.kanbanCategory !== 'active' && !matches(card.dataset, filter);
                if (card.hidden) selection().delete(card.dataset.taskId);
            });
            const visible = all.filter(card => !card.hidden), selected = visible.filter(card => selection().has(card.dataset.taskId));
            all.forEach(card => {
                const chosen = selection().has(card.dataset.taskId);
                card.classList.toggle('task-selected', chosen);
                const label = card.querySelector('[data-kanban-checkbox]');
                if (label) { label.classList.toggle('hidden', !selected.length); label.querySelector('input').checked = chosen; }
            });
            col.querySelectorAll('[data-kanban-select]').forEach(button => { button.textContent = selected.length ? 'Clear selection' : 'Select all'; });
            col.querySelectorAll('[data-kanban-scope]').forEach(el => { el.textContent = 'Actions for ' + (selected.length ? selected.length + ' selected' : Object.keys(filter).length ? visible.length + ' filtered' : 'all ' + visible.length); });
            col.querySelectorAll('[data-kanban-action]').forEach(el => { el.disabled = !!window.kanbanBatchRunning || !visible.length; });
            const summary = col.querySelector('[data-kanban-summary]');
            summary.replaceChildren();
            document.querySelectorAll('[data-kanban-filter][data-column="' + col.dataset.kanbanCategory + '"]').forEach(button => {
                const active = filter[button.dataset.kanbanFilter];
                const checked = (active ? active.value : '') === button.dataset.value;
                button.setAttribute('aria-pressed', String(checked));
                button.classList.toggle('active', checked);
            });
            if (selected.length) summary.append(textElement('span', selected.length + ' selected '));
            if (Object.keys(filter).length) summary.append(textElement('span', visible.length + ' of ' + all.length + ' '));
            Object.entries(filter).forEach(([name, value]) => {
                const chip = textElement('button', value.label + ' ×'); chip.className = 'btn btn-xs btn-ghost';
                chip.onclick = () => { delete filter[name]; window.kanbanClearSelection(); refresh(); }; summary.append(chip);
            });
            renderProgress(col);
        });
    }
    window.kanbanRefresh = refresh;
    window.kanbanSelectAll = button => {
        const col = column(button.dataset.column); if (!col) return;
        const clear = cards(col).some(card => selection().has(card.dataset.taskId));
        window.kanbanClearSelection();
        if (!clear) cards(col).filter(card => !card.hidden).forEach(card => selection().add(card.dataset.taskId));
        refresh();
    };
    window.kanbanToggleCard = input => {
        const card = input.closest('.card[data-task-id]'), col = card.closest('[data-kanban-category]');
        if (Array.from(selection()).some(id => { const other = document.getElementById('task-' + id); return other && !col.contains(other); })) window.kanbanClearSelection();
        if (input.checked) selection().add(card.dataset.taskId); else selection().delete(card.dataset.taskId);
        refresh();
    };
    window.kanbanSetFilter = button => {
        const col = column(button.dataset.column); if (!col || col.dataset.kanbanCategory === 'active') return;
        const filter = filters.get(key(col)) || {}, name = button.dataset.kanbanFilter;
        if (name === 'clear') filters.delete(key(col));
        else {
            if (button.dataset.value) filter[name] = { value: button.dataset.value, label: button.textContent };
            else delete filter[name];
            filters.set(key(col), filter);
        }
        window.kanbanClearSelection(); refresh();
    };
    async function request(path, options) {
        const response = await fetch(path, options);
        if (response.redirected) throw new Error('Session changed. Reload the page.');
        const html = await response.text();
        let toast;
        try { toast = JSON.parse(response.headers.get('HX-Trigger') || '{}').openvibelyToast; } catch (_) {}
        if (!response.ok || toast && ['failed', 'error'].includes(toast.status)) throw new Error(toast && toast.message || 'Request failed (' + response.status + ')');
        return { html, toast };
    }
    window.confirmDeleteAllTasks = () => {
        const pending = pendingDelete; pendingDelete = null;
        const modal = document.getElementById('delete_all_tasks_confirm_modal');
        modal.__openVibelyDestructiveTrigger = null;
        modal.close();
        if (document.activeElement && document.activeElement.closest('.dropdown')) document.activeElement.blur();
        if (pending) window.kanbanBatch(pending.button, pending.targets);
    };
    window.kanbanBatch = async (button, confirmedTargets) => {
        if (window.kanbanBatchRunning || window._taskCardActionRequest) return;
        const col = column(button.dataset.column); if (!col) return;
        const action = button.dataset.kanbanAction, project = col.dataset.projectId;
        const targets = confirmedTargets || scope(col).map(card => ({ id: card.dataset.taskId, title: card.dataset.taskTitle, status: card.dataset.taskStatus, merge: card.dataset.taskMerge }));
        if (!targets.length) return;
        if (action === 'delete' && !confirmedTargets) {
            pendingDelete = { button, targets };
            window.openDestructiveConfirmDialog('delete_all_tasks_confirm_modal', 'delete_all_tasks_confirm_name', targets.length + ' tasks');
            return;
        }
        const batch = { label: button.textContent.trim(), total: targets.length, done: 0, success: 0, skipped: 0, failed: 0, lines: [], running: true, stop: false };
        const batchKey = key(col);
        results.set(batchKey, batch); window.kanbanBatchRunning = true;
        if (window.closeKanbanMenu) window.closeKanbanMenu(null, false);
        refresh();
        try {
            for (const task of targets) {
                if (batch.stop) break;
                try {
                    let path = '/tasks/' + encodeURIComponent(task.id), values = { project_id: project };
                    const git = ['merge', 'ff', 'squash', 'rebase', 'pr'].includes(action);
                    let skip = '';
                    if (git) {
                        const options = await request(path + '/card/merge-options?project_id=' + encodeURIComponent(project));
                        const doc = new DOMParser().parseFromString(options.html, 'text/html');
                        const control = doc.querySelector('[data-merge-type="' + action + '"]');
                        if (action === 'pr' && doc.querySelector('[data-has-pull-request="true"]')) skip = 'PR already exists';
                        else if (action !== 'pr' && task.merge === 'merged') skip = 'Already merged';
                        else if (!control || control.disabled) skip = 'Not eligible';
                        else {
                            path += '/worktree/' + control.dataset.mergeEndpoint;
                            values = { ...values, merge_source: 'task_card', merge_type: action, target_branch: control.dataset.targetBranch || '' };
                        }
                    } else if (action === 'run' && !['pending', 'failed', 'cancelled'].includes(task.status)) skip = 'Not runnable';
                    else if (action === 'cancel' && !['pending', 'queued', 'running', 'blocked'].includes(task.status)) skip = 'Already stopped';
                    if (skip) { batch.skipped++; batch.lines.push(task.title + ': ' + skip); }
                    else {
                        if (!git && action !== 'delete') path += '/' + action;
                        const result = await request(path + '?project_id=' + encodeURIComponent(project), { method: action === 'delete' ? 'DELETE' : 'POST', headers: { 'HX-Request': 'true', 'Content-Type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams(values) });
                        batch.success++; batch.lines.push(task.title + ': ' + (result.toast ? result.toast.message : 'Done'));
                    }
                } catch (error) { batch.failed++; batch.lines.push(task.title + ': ' + error.message); if (['merge', 'squash', 'rebase'].includes(action)) batch.stop = true; }
                batch.done++; renderProgress(currentColumn(batchKey));
            }
        } finally {
            batch.running = false; window.kanbanBatchRunning = false;
            window.kanbanClearSelection(); refresh();
            window.dispatchEvent(new CustomEvent('kanban-refresh-unblocked'));
            if (currentColumn(batchKey) && window.htmx) window.htmx.ajax('GET', '/tasks?project_id=' + encodeURIComponent(project), { target: '#kanban-board', select: '#kanban-board', swap: 'outerHTML' });
        }
    };
    document.addEventListener('DOMContentLoaded', refresh);
    document.addEventListener('htmx:afterSwap', refresh);
    document.addEventListener('htmx:afterSettle', refresh);
    document.addEventListener('task-pointer-dragend', refresh);
    document.addEventListener('click', event => {
        if (!window.kanbanBatchRunning) return;
        const dropdown = window.kanbanDropdownForTarget && window.kanbanDropdownForTarget(event.target);
        if (dropdown && dropdown.closest('#kanban-board')) {
            event.preventDefault(); event.stopImmediatePropagation();
        }
    }, true);
})();
