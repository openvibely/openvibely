package components

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestChatEarlierLifecycleHydratesFinalPageWithoutLoader(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for lifecycle regression test")
	}
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	source := buf.String()
	helperStart := strings.Index(source, "function getEarlierContainerForElement(")
	helperEnd := strings.Index(source, "function getFirstVisibleExecutionPair(")
	start := strings.Index(source, "window.bindChatEarlierHTMXLifecycle = function()")
	end := strings.Index(source, "function clearChatEarlierRestoreState(")
	if helperStart < 0 || helperEnd <= helperStart || start < 0 || end <= start {
		t.Fatal("missing earlier-message lifecycle helpers")
	}
	script := `
const assert = require('node:assert/strict');
const listeners = {};
const container = { id: 'task-thread-messages' };
const window = {};
const document = {
  body: { addEventListener(name, callback) { (listeners[name] ||= []).push(callback); } },
  getElementById(id) { return id === container.id ? container : null; }
};
let hydrated = 0, restored = 0, prepared = 0;
window.prepareChatEarlierSwap = () => prepared++;
window.afterChatEarlierSwap = loader => {
  assert.equal(getEarlierContainerForElement(loader), container);
  hydrated++;
};
window.restoreChatEarlierScroll = value => { assert.equal(value, container); restored++; };
function loader() {
  return { isConnected: false,
    matches(selector) { return selector === '[data-earlier-loader="true"]'; },
    getAttribute(name) { return name === 'data-container-id' ? container.id : null; }
  };
}
function emit(name, elt, target) {
  for (const callback of listeners[name] || []) callback({ detail: { elt, target } });
}
` + source[helperStart:helperEnd] + source[start:end] + `
window.bindChatEarlierHTMXLifecycle();
window.bindChatEarlierHTMXLifecycle();
// outerHTML removes the old loader; the final response contains only message rows.
// HTMX dispatches swap/settle on EACH inserted row, with the detached loader as target.
const oldLoader = loader();
const rows = Array.from({length: 4}, () => ({ matches() { return false; } }));
emit('htmx:beforeRequest', oldLoader, oldLoader);
for (const row of rows) emit('htmx:afterSwap', row, oldLoader);
for (const row of rows) emit('htmx:afterSettle', row, oldLoader);
assert.equal(hydrated, 1, 'final page must hydrate once even without a replacement loader');
assert.equal(restored, 1, 'final page must restore scroll once');
// Intermediate pages still have a replacement loader and must behave identically.
const nextLoader = loader();
emit('htmx:beforeRequest', nextLoader, nextLoader);
for (const elt of [loader(), ...rows]) emit('htmx:afterSwap', elt, nextLoader);
for (const elt of [loader(), ...rows]) emit('htmx:afterSettle', elt, nextLoader);
assert.equal(hydrated, 2);
assert.equal(restored, 2);
// Unrelated HTMX swaps must not rehydrate the transcript.
emit('htmx:afterSwap', rows[0], rows[0]);
emit('htmx:afterSettle', rows[0], rows[0]);
assert.equal(hydrated, 2);
assert.equal(restored, 2);
// A retry on the same loader starts a fresh lifecycle.
emit('htmx:beforeRequest', nextLoader, nextLoader);
emit('htmx:afterSwap', rows[0], nextLoader);
emit('htmx:afterSettle', rows[0], nextLoader);
assert.equal(hydrated, 3);
assert.equal(restored, 3);
assert.equal(prepared, 3);
`
	cmd := exec.Command(node, "-")
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("earlier-message lifecycle regression: %v\n%s", err, output)
	}
}
