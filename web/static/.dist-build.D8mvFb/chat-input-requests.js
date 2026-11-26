(function () {
  'use strict';

  // Question cards work in any chat surface; each surface places and refreshes its own cards.
  window._chatInputDrafts = window._chatInputDrafts || {};

  function chatInputCardProjectID(card) {
    var own = card.getAttribute('data-project-id');
    if (own) return own;
    var owner = card.parentElement && card.parentElement.closest('[data-project-id]');
    return owner ? owner.getAttribute('data-project-id') || '' : '';
  }

  function initChatInputRequestCards(scope) {
    (scope || document).querySelectorAll('[data-chat-input-request-id]').forEach(function(card) {
      if (card._chatInputInitialized) return;
      card._chatInputInitialized = true;
      showChatInputQuestion(card, applyChatInputDraft(card));
    });
  }

  function showChatInputQuestion(card, index) {
    if (!card) return;
    var groups = Array.prototype.slice.call(card.querySelectorAll('[data-chat-input-question-index]'));
    if (!groups.length) return;
    var nextIndex = Math.max(0, Math.min(Number(index) || 0, groups.length - 1));
    groups.forEach(function(group, groupIndex) {
      group.classList.toggle('hidden', groupIndex !== nextIndex);
    });
    card.setAttribute('data-current-question-index', String(nextIndex));
    saveChatInputDraft(card);
    var position = card.querySelector('[data-chat-input-position]');
    if (position) position.textContent = (nextIndex + 1) + ' of ' + groups.length;
    var previous = card.querySelector('[data-chat-input-nav="previous"]');
    var next = card.querySelector('[data-chat-input-nav="next"], [data-chat-input-nav="submit"]');
    if (previous) {
      previous.disabled = nextIndex === 0;
      previous.classList.toggle('invisible', groups.length === 1);
    }
    if (next) {
      var isLast = nextIndex === groups.length - 1;
      next.textContent = isLast ? 'Submit' : 'Next';
      next.setAttribute('data-chat-input-nav', isLast ? 'submit' : 'next');
      next.setAttribute('aria-label', isLast ? 'Submit answers' : 'Next question');
      next.disabled = isLast ? !chatInputRequestIsComplete(card) : !chatInputQuestionAnswer(groups[nextIndex]);
    }
    var legend = groups[nextIndex].querySelector('legend');
    if (legend) legend.focus && legend.focus();
  }

  function navigateChatInputRequest(button) {
    var card = button ? button.closest('[data-chat-input-request-id]') : null;
    if (!card || card.getAttribute('data-completed') === 'true') return;
    var action = button.getAttribute('data-chat-input-nav') || '';
    if (action === 'submit') {
    submitChatInputRequest(button);
    return;
    }
    var current = Number(card.getAttribute('data-current-question-index') || 0);
    if (action === 'recommended') {
                                var groups = Array.from(card.querySelectorAll('[data-chat-input-question-id]'));
                                var unresolved = groups.findIndex(function(group) { return group.dataset.chatInputQuestionKind === 'projects' && !chatInputQuestionAnswer(group); });
                                if (unresolved >= 0) {
                                    groups.forEach(function(group) { if (group.dataset.chatInputQuestionKind !== 'projects') { var first = group.querySelector('[data-chat-input-option]'); if(first) markChatInputSelection(group, first); } });
                                    showChatInputQuestion(card, unresolved);
                                    return;
                                }
                                submitChatInputRequest(button, chatInputRequestRecommendedAnswers(card));
      return;
    }
    showChatInputQuestion(card, current + (action === 'previous' ? -1 : 1));
  }

  function chatInputQuestionAnswer(group) {
    if (!group) return null;
    var questionID = group.getAttribute('data-chat-input-question-id') || '';
    if (!questionID) return null;
    if (group.getAttribute('data-selected-kind') === 'custom') {
      var input = group.querySelector('[data-chat-input-custom-answer]');
      var customAnswer = input ? String(input.value || '').trim() : '';
      return customAnswer ? {question_id: questionID, custom_answer: customAnswer} : null;
    }
    var label = group.getAttribute('data-selected-label') || '';
                            if (group.dataset.chatInputQuestionKind === 'projects') {
                                var ids = Array.from(group.querySelectorAll('[data-project-choice]')).filter(function(button) { return label === 'All projects' || button.getAttribute('aria-pressed') === 'true'; }).map(function(button) { return button.dataset.projectChoice; });
                                return label && ids.length ? {question_id: questionID, label: label, project_ids: ids} : null;
                            }
                            return label ? {question_id: questionID, label: label} : null;
  }

  function chatInputRequestAnswers(card) {
    var answers = [];
    card.querySelectorAll('[data-chat-input-question-id]').forEach(function(group) {
      var answer = chatInputQuestionAnswer(group);
      if (answer) answers.push(answer);
    });
    return answers;
  }

    function chatInputRequestRecommendedAnswers(card) {
    var answers = [];
    var complete = true;
    card.querySelectorAll('[data-chat-input-question-id]').forEach(function(group) {
      var questionID = group.getAttribute('data-chat-input-question-id') || '';
      if (group.dataset.chatInputQuestionKind === 'projects') {
                                    var answer = chatInputQuestionAnswer(group);
                                    if (answer) answers.push(answer); else complete = false;
                                    return;
                                }
                                var recommended = group.querySelector('[data-chat-input-option]:not([data-chat-input-custom-option])');
      var label = recommended ? (recommended.getAttribute('data-option-label') || '') : '';
      if (!questionID || !label) {
        complete = false;
        return;
      }
      answers.push({question_id: questionID, label: label});
    });
    return complete ? answers : [];
    }

  function chatInputRequestIsComplete(card) {
    return chatInputRequestAnswers(card).length === card.querySelectorAll('[data-chat-input-question-id]').length;
  }

  function selectChatInputOption(button) {
    if (!button || button.disabled || button.getAttribute('aria-disabled') === 'true') return;
    var card = button.closest('[data-chat-input-request-id]');
    var fieldset = button.closest('[data-chat-input-question-id]');
    if (!card || !fieldset || card.getAttribute('data-completed') === 'true' || card.getAttribute('data-submitted') === 'true') return;
    var isCustom = button.hasAttribute('data-chat-input-custom-option');
    markChatInputSelection(fieldset, button);
                            if (fieldset.dataset.chatInputQuestionKind === 'projects') {
                                showChatInputQuestion(card, Number(card.dataset.currentQuestionIndex || 0));
                                if (button.dataset.optionLabel === 'Specific projects') { fieldset.querySelector('[data-project-search]').focus(); projectSearchMenu(fieldset.querySelector('[data-project-picker]')).open(); }
                                return;
                            }
                            var status = card.querySelector('[role="status"]');
    if (status) status.classList.add('hidden');
    showChatInputQuestion(card, Number(card.getAttribute('data-current-question-index') || 0));
    if (isCustom) {
      var customInput = fieldset.querySelector('[data-chat-input-custom-answer]');
      if (customInput) customInput.focus();
    } else {
      var currentIndex = Number(card.getAttribute('data-current-question-index') || 0);
      var questionCount = card.querySelectorAll('[data-chat-input-question-index]').length;
      if (currentIndex < questionCount - 1) {
        window.setTimeout(function() {
          if (card.getAttribute('data-current-question-index') === String(currentIndex)) showChatInputQuestion(card, currentIndex + 1);
        }, 300);
      }
    }
  }

  function completeChatInputRequest(card) {
    window._chatInputRevision = (window._chatInputRevision || 0) + 1;
    if (!card) return;
    delete window._chatInputDrafts[card.getAttribute('data-chat-input-request-id') || ''];
    card.remove();
  }

  function markChatInputSelection(fieldset, button) {
    var isCustom = button.hasAttribute('data-chat-input-custom-option');
    fieldset.setAttribute('data-selected-kind', isCustom ? 'custom' : 'option');
    fieldset.setAttribute('data-selected-label', isCustom ? '' : (button.getAttribute('data-option-label') || ''));
    fieldset.querySelectorAll('[data-chat-input-option]').forEach(function(btn) {
      var selected = btn === button;
      btn.classList.toggle('chat-input-option-selected', selected);
      btn.setAttribute('aria-pressed', selected ? 'true' : 'false');
    });
    var picker = fieldset.querySelector('[data-project-picker]');
                            if (picker) picker.classList.toggle('hidden', button.dataset.optionLabel !== 'Specific projects');
                            var customWrap = fieldset.querySelector('[data-chat-input-custom-wrap]');
    if (customWrap) customWrap.classList.toggle('hidden', !isCustom);
  }

                        function renderProjectQuestionChips(group) {
                            var chips = group.querySelector('[data-project-chips]');
                            if (!chips) return;
                            chips.replaceChildren();
                            group.querySelectorAll('[data-project-choice]').forEach(function(button) {
                                var selected = button.getAttribute('aria-pressed') === 'true';
                                button.querySelector('[data-project-check]').textContent = selected ? '☑' : '□';
                                if (!selected) return;
                                var chip = document.createElement('button');
                                chip.type = 'button'; chip.className = 'btn btn-xs';
                                chip.dataset.projectRemove = button.dataset.projectChoice;
                                chip.textContent = button.dataset.projectName + ' ×';
                                chip.setAttribute('aria-label', 'Remove ' + button.dataset.projectName);
                                chips.appendChild(chip);
                            });
                        }

  function saveChatInputDraft(card) {
    var requestId = card.getAttribute('data-chat-input-request-id') || '';
    if (!requestId) return;
    var selections = {};
    card.querySelectorAll('[data-chat-input-question-id]').forEach(function(group) {
      var kind = group.getAttribute('data-selected-kind') || '';
      if (!kind) return;
      var input = group.querySelector('[data-chat-input-custom-answer]');
      selections[group.getAttribute('data-chat-input-question-id')] = {
        kind: kind,
                                    projects: Array.from(group.querySelectorAll('[data-project-choice][aria-pressed="true"]')).map(function(button) {return button.dataset.projectChoice;}),
        label: group.getAttribute('data-selected-label') || '',
        custom: input ? String(input.value || '') : ''
      };
    });
    window._chatInputDrafts[requestId] = {projectId: chatInputCardProjectID(card), index: Number(card.getAttribute('data-current-question-index') || 0), selections: selections};
  }

  function applyChatInputDraft(card) {
    var draft = window._chatInputDrafts[card.getAttribute('data-chat-input-request-id') || ''];
    if (!draft) return 0;
    card.querySelectorAll('[data-chat-input-question-id]').forEach(function(group) {
      var saved = draft.selections[group.getAttribute('data-chat-input-question-id')];
      if (!saved) return;
      var match = null;
      group.querySelectorAll('[data-chat-input-option]').forEach(function(btn) {
        if (match) return;
        var isCustomBtn = btn.hasAttribute('data-chat-input-custom-option');
        if (saved.kind === 'custom' ? isCustomBtn : (!isCustomBtn && btn.getAttribute('data-option-label') === saved.label)) match = btn;
      });
      if (!match) return;
      markChatInputSelection(group, match);
                                group.querySelectorAll('[data-project-choice]').forEach(function(button) { button.setAttribute('aria-pressed', String((saved.projects || []).includes(button.dataset.projectChoice))); });
                                renderProjectQuestionChips(group);
      var input = group.querySelector('[data-chat-input-custom-answer]');
      if (input && saved.kind === 'custom') input.value = saved.custom;
    });
    return draft.index || 0;
  }

function submitChatInputRequest(button, overrideAnswers) {
	if (!button || button.disabled || button.getAttribute('aria-disabled') === 'true') return;
	var card = button.closest('[data-chat-input-request-id]');
	if (!card || card.getAttribute('data-submitted') === 'true') return;
	var status = card.querySelector('[role="status"]');
	var answers = Array.isArray(overrideAnswers) ? overrideAnswers : chatInputRequestAnswers(card);
	var usingRecommended = Array.isArray(overrideAnswers);
	var groups = Array.prototype.slice.call(card.querySelectorAll('[data-chat-input-question-index]'));
	if ((!usingRecommended && !chatInputRequestIsComplete(card)) || (usingRecommended && answers.length !== groups.length)) {
		if (status) {
			status.classList.remove('hidden', 'text-error');
			status.textContent = 'Answer every question before submitting.';
		}
		var nextUnanswered = groups.findIndex(function(group) { return !chatInputQuestionAnswer(group); });
		if (nextUnanswered >= 0) showChatInputQuestion(card, nextUnanswered);
		return;
	}
	var requestId = card.getAttribute('data-chat-input-request-id') || '';
	var projectID = chatInputCardProjectID(card);
	var previousStatus = status ? {className: status.className, text: status.textContent} : null;
	card.setAttribute('data-submitted', 'true');
	card.querySelectorAll('button, textarea, input').forEach(function(control) { control.disabled = true; });
	card.querySelectorAll('[role="button"][data-chat-input-option], [role="button"][data-chat-input-nav]').forEach(function(control) { control.setAttribute('aria-disabled', 'true'); control.classList.add('pointer-events-none'); });
	if (status) {
		status.classList.remove('hidden', 'text-error');
		status.textContent = usingRecommended ? 'Submitting recommended answers…' : 'Submitting answers…';
	}
	fetch('/chat/input-requests/' + encodeURIComponent(requestId) + '/answer', {
		method: 'POST',
		headers: {'Content-Type': 'application/json'},
		body: JSON.stringify({project_id: projectID || '', answers: answers})
	}).then(async function(resp) {
		if (await window.openVibelyPermissionErrors?.fetch(resp)) {
			var error = new Error('Permission denied');
			error.toastOnly = true;
			throw error;
		}
		if (!resp.ok) throw new Error('Unable to submit answer');
		completeChatInputRequest(card, {questions: Array.prototype.slice.call(card.querySelectorAll('[data-chat-input-question-id]')).map(function(group) {
			return {id: group.getAttribute('data-chat-input-question-id') || '', question: (group.querySelector('legend') || {}).textContent || ''};
		}), answers: answers});
	}).catch(function(err) {
		card.removeAttribute('data-submitted');
		card.querySelectorAll('button, textarea, input').forEach(function(control) { control.disabled = false; });
		card.querySelectorAll('[role="button"][data-chat-input-option], [role="button"][data-chat-input-nav]').forEach(function(control) { control.removeAttribute('aria-disabled'); control.classList.remove('pointer-events-none'); });
		if (err && err.toastOnly) {
			if (status && previousStatus) {
				status.className = previousStatus.className;
				status.textContent = previousStatus.text;
			}
			return;
		}
		showChatInputQuestion(card, Number(card.getAttribute('data-current-question-index') || 0));
		if (status) {
			status.classList.remove('hidden');
			status.classList.add('text-error');
			status.textContent = err && err.message ? err.message : 'Unable to submit answer';
		}
	});
}

  function bindChatInputRequestControls() {
  if (!window._chatInputRequestClickBound) {
    window._chatInputRequestClickBound = true;
                            const projectSearchMenus=new WeakMap();
                            window.projectSearchMenu=function(picker) {
                                let menu=projectSearchMenus.get(picker);
                                if(!menu) {
                                    const results=picker.querySelector('[data-project-results]');
                                    menu=window.ovMenuController.create({trigger:()=>picker.querySelector('[data-project-search]'),panels:[picker],isOpen:()=>!results.classList.contains('hidden')&&!picker.closest('[hidden],.hidden'),open:()=>results.classList.remove('hidden'),close:()=>results.classList.add('hidden')});
                                    projectSearchMenus.set(picker,menu);
                                }
                                return menu;
                            };
                            document.body.addEventListener('click',function(event) {
                                if(event.target.matches('[data-project-search]'))projectSearchMenu(event.target.closest('[data-project-picker]')).open(event);
                            });
                            document.body.addEventListener('keydown',function(event) {
                                if(event.target.matches('[data-project-search]') && event.key==='ArrowDown') {event.preventDefault();projectSearchMenu(event.target.closest('[data-project-picker]')).open(event);}
                            });
                            document.body.addEventListener('input', function(event) {
                                if (!event.target.matches('[data-project-search]')) return;
                                var picker = event.target.closest('[data-project-picker]'), query = event.target.value.toLowerCase().trim(), count = 0;
                                projectSearchMenu(picker).open();
                                picker.querySelectorAll('[data-project-choice]').forEach(function(button) { var match = button.dataset.projectName.toLowerCase().includes(query); button.classList.toggle('hidden', !match); if (match) count++; });
                                picker.querySelector('[data-project-empty]').classList.toggle('hidden', count !== 0);
                            });
                            document.body.addEventListener('click', function(event) {
                                var choice = event.target.closest('[data-project-choice], [data-project-remove]');
                                if (choice) {
                                    event.preventDefault();
                                    var group = choice.closest('[data-chat-input-question-id]'), card = group.closest('[data-chat-input-request-id]');
                                    if (card.dataset.submitted === 'true' || card.dataset.completed === 'true') return;
                                    var id = choice.dataset.projectChoice || choice.dataset.projectRemove;
                                    var button = Array.from(group.querySelectorAll('[data-project-choice]')).find(function(button) { return button.dataset.projectChoice === id; });
                                    button.setAttribute('aria-pressed', String(!choice.dataset.projectRemove && button.getAttribute('aria-pressed') !== 'true'));
                                    renderProjectQuestionChips(group);
                                    showChatInputQuestion(card, Number(card.dataset.currentQuestionIndex || 0));
                                    group.querySelector('[data-project-search]').focus();
                                    projectSearchMenu(group.querySelector('[data-project-picker]')).open();
                                    return;
                                }
                                var navigationButton = event.target && event.target.closest ? event.target.closest('[data-chat-input-nav]') : null;
      if (navigationButton) {
        event.preventDefault();
        navigateChatInputRequest(navigationButton);
        return;
      }
      var button = event.target && event.target.closest ? event.target.closest('[data-chat-input-option]') : null;
      if (!button) return;
      event.preventDefault();
      selectChatInputOption(button);
    });
    document.body.addEventListener('keydown', function(event) {
      var control = event.target && event.target.closest ? event.target.closest('[role="button"][data-chat-input-option], [role="button"][data-chat-input-nav]') : null;
      if (!control || control.getAttribute('aria-disabled') === 'true' || (event.key !== 'Enter' && event.key !== ' ')) return;
      event.preventDefault();
      control.click();
    });
    document.body.addEventListener('input', function(event) {
      var input = event.target && event.target.closest ? event.target.closest('[data-chat-input-custom-answer]') : null;
      if (!input) return;
      var card = input.closest('[data-chat-input-request-id]');
      if (card) showChatInputQuestion(card, Number(card.getAttribute('data-current-question-index') || 0));
    });
  }
  }
  if (document.body) bindChatInputRequestControls();
  else document.addEventListener('DOMContentLoaded', bindChatInputRequestControls);

  window.bindChatInputRequestControls = bindChatInputRequestControls;
  window.initChatInputRequestCards = initChatInputRequestCards;
  window.showChatInputQuestion = showChatInputQuestion;
  window.navigateChatInputRequest = navigateChatInputRequest;
  window.chatInputQuestionAnswer = chatInputQuestionAnswer;
  window.chatInputRequestAnswers = chatInputRequestAnswers;
  window.chatInputRequestRecommendedAnswers = chatInputRequestRecommendedAnswers;
  window.chatInputRequestIsComplete = chatInputRequestIsComplete;
  window.selectChatInputOption = selectChatInputOption;
  window.completeChatInputRequest = completeChatInputRequest;
  window.markChatInputSelection = markChatInputSelection;
  window.renderProjectQuestionChips = renderProjectQuestionChips;
  window.saveChatInputDraft = saveChatInputDraft;
  window.applyChatInputDraft = applyChatInputDraft;
  window.submitChatInputRequest = submitChatInputRequest;
})();
