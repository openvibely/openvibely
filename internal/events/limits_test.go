package events

import (
	"testing"

	"github.com/openvibely/openvibely/internal/livelimits"
	"github.com/stretchr/testify/require"
)

func TestSubscriberBudgetDefaultsAndNoLegacyCap(t *testing.T) {
	task := NewBroadcaster()
	chat := NewChatBroadcaster()
	files := NewFileChangeBroadcaster()
	hub := NewExecutionStreamHub()
	require.Equal(t, livelimits.DefaultLiveSubscriptions, task.core.maxSubscribers)
	require.Equal(t, livelimits.DefaultLiveSubscriptions, chat.core.maxSubscribers)
	require.Equal(t, livelimits.DefaultLiveSubscriptions, files.core.maxSubscribers)
	require.Equal(t, livelimits.DefaultExecutionSubscriptions, hub.maxSubscribers)
	// Exercise every broadcaster past the former shared 50-subscriber ceiling.
	for i := 0; i < 60; i++ {
		ts, err := task.SubscribeProject("project")
		require.NoError(t, err)
		defer task.Unsubscribe(ts)
		cs, err := chat.SubscribeProject("project")
		require.NoError(t, err)
		defer chat.Unsubscribe(cs)
		fs, err := files.SubscribeTask("task")
		require.NoError(t, err)
		defer files.Unsubscribe(fs)
		_, unsubscribe, err := hub.Subscribe("exec")
		require.NoError(t, err)
		defer unsubscribe()
	}
}
