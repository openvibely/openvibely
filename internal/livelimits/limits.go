// Package livelimits defines startup resource budgets for the shared live transport.
package livelimits

const (
	DefaultConnections                = 500
	DefaultSubscriptionsPerConnection = 200
	DefaultLiveSubscriptions          = 5000
	DefaultExecutionSubscriptions     = 25000
)

// Limits are per backend instance, except SubscriptionsPerConnection, which counts
// live and execution subscriptions together within one browser tab/native window.
// These are admission budgets, not a guarantee of throughput or agent concurrency.
type Limits struct {
	Connections                int
	SubscriptionsPerConnection int
	LiveSubscriptions          int
	ExecutionSubscriptions     int
}

// WithDefaults fills unset or invalid values with finite defaults.
func (l Limits) WithDefaults() Limits {
	if l.Connections <= 0 {
		l.Connections = DefaultConnections
	}
	if l.SubscriptionsPerConnection <= 0 {
		l.SubscriptionsPerConnection = DefaultSubscriptionsPerConnection
	}
	if l.LiveSubscriptions <= 0 {
		l.LiveSubscriptions = DefaultLiveSubscriptions
	}
	if l.ExecutionSubscriptions <= 0 {
		l.ExecutionSubscriptions = DefaultExecutionSubscriptions
	}
	return l
}
