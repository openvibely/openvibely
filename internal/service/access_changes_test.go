package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccessChangesTargetUserAndProject(t *testing.T) {
	var hub accessHub
	bob := hub.watch("bob")
	defer bob.Close()
	carol := hub.watch("carol")
	defer carol.Close()
	other := hub.watch("bob")
	defer other.Close()
	bob.SetProjects(map[string]bool{"p": true}, false)
	carol.SetProjects(map[string]bool{"p": true}, false)
	other.SetProjects(map[string]bool{"other": true}, false)
	hub.notify(AccessChange{UserID: "bob", ProjectID: "p"})
	require.Len(t, bob.Drain(), 1)
	require.Empty(t, carol.Drain())
	require.Empty(t, other.Drain())
	select {
	case <-carol.C():
		t.Fatal("unrelated user was woken")
	default:
	}
	select {
	case <-other.C():
		t.Fatal("unrelated project was woken")
	default:
	}
	hub.notify(AccessChange{ProjectID: "p"})
	require.Len(t, bob.Drain(), 1)
	require.Len(t, carol.Drain(), 1)
	require.Empty(t, other.Drain())
	hub.notify(AccessChange{UserID: "bob", CloseConnections: true})
	require.True(t, bob.Drain()[0].CloseConnections)
	require.True(t, other.Drain()[0].CloseConnections)
	require.Empty(t, carol.Drain())
}

func TestAccessChangesOverflowPreservesLogout(t *testing.T) {
	var hub accessHub
	w := hub.watch("bob")
	defer w.Close()
	hub.notify(AccessChange{UserID: "bob", CloseConnections: true})
	for i := 0; i < 130; i++ {
		hub.notify(AccessChange{UserID: "bob", ProjectID: "p"})
	}
	changes := w.Drain()
	require.LessOrEqual(t, len(changes), 64)
	require.True(t, changes[0].CloseConnections)
}
