package transcript

// ExchangeTracker owns the messages produced by one execution. It does not
// retain or compare previous executions. Clients supply their wire-format
// entries at the point they append, edit, or replace them.
// The tracker is used by one agent loop; borrowed Items must not be mutated.
type ExchangeTracker[T any] struct {
	items    []T
	changed  int
	replaces bool
}

func (t *ExchangeTracker[T]) Items() []T            { return t.items }
func (t *ExchangeTracker[T]) Len() int              { return len(t.items) }
func (t *ExchangeTracker[T]) FirstChanged() int     { return t.changed }
func (t *ExchangeTracker[T]) ReplacesHistory() bool { return t.replaces }

func (t *ExchangeTracker[T]) Append(items ...T) {
	t.changed = min(t.changed, len(t.items))
	t.items = append(t.items, items...)
}

// ReplaceTail also handles edits to the current exchange's context or tool
// results. Unlike ReplaceHistory, it never changes the history boundary.
func (t *ExchangeTracker[T]) ReplaceTail(start int, items ...T) {
	t.changed = min(t.changed, start)
	next := append([]T(nil), t.items[:start]...)
	t.items = append(next, items...)
}

func (t *ExchangeTracker[T]) Set(index int, item T) {
	t.items[index] = item
	t.changed = min(t.changed, index)
}

// ReplaceHistory records an explicit compaction, repair, or resumed execution.
// Subsequent appends remain part of that replacement until the execution ends.
func (t *ExchangeTracker[T]) ReplaceHistory(items []T) {
	t.items = append([]T(nil), items...)
	t.changed = 0
	t.replaces = true
}

// Committed advances the write boundary only after a successful save. Optional
// serialization-only entries (e.g. pending images) do not become owned history.
func (t *ExchangeTracker[T]) Committed() { t.changed = len(t.items) }
