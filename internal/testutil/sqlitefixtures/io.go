package sqlitefixtures

import (
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

type ioCounter struct{ counter *testutil.SQLStatementCounter }

func NewIOCounter(counter *testutil.SQLStatementCounter) storefixture.IOCounter {
	return &ioCounter{counter: counter}
}
func (c *ioCounter) Reset()                  { c.counter.Reset() }
func (c *ioCounter) SetEnabled(enabled bool) { c.counter.SetEnabled(enabled) }
func (c *ioCounter) Snapshot() storefixture.IOStats {
	return storefixture.IOStats{Requests: len(c.counter.Statements()), ReadTextBytes: c.counter.SelectedTextBytes()}
}
