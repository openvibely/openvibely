package remote

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/remoteid"
)

func TestReciprocalHubsDoNotRepeatEvents(t *testing.T) {
	for _, kind := range []string{"task_status_changed", "chat_response_done"} {
		t.Run(kind, func(t *testing.T) {
			a := &Conn{serverID: server, userID: "alice", hub: &Hub{}}
			b := &Conn{serverID: "fedcba9876543210", userID: "bob", hub: &Hub{}}
			deliveries := 0
			// Simulate each server broadcasting imported events to the other server's
			// live subscription, including the JSON round trip that loses RemoteUserID.
			relay := func(peer *Conn, event any) {
				deliveries++
				if deliveries > 4 {
					t.Fatal("event is circulating between reciprocal connections")
				}
				data, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				peer.dispatch(Frame{ID: liveSubscriptionID, Event: kind, Data: data})
			}
			a.hub.OnTask = func(e events.TaskEvent) { relay(b, e) }
			b.hub.OnTask = func(e events.TaskEvent) { relay(a, e) }
			a.hub.OnChat = func(e events.ChatEvent) { relay(b, e) }
			b.hub.OnChat = func(e events.ChatEvent) { relay(a, e) }
			data := []byte(`{"type":"` + kind + `","project_id":"project","task_id":"task","exec_id":"exec","status":"completed"}`)
			a.dispatch(Frame{ID: liveSubscriptionID, Event: kind, Data: data})
			if deliveries != 1 {
				t.Fatalf("got %d deliveries, want one", deliveries)
			}
			// A later genuine completion must still arrive, even with the same IDs.
			a.dispatch(Frame{ID: liveSubscriptionID, Event: kind, Data: data})
			if deliveries != 2 {
				t.Fatalf("got %d deliveries after another completion, want two", deliveries)
			}
		})
	}
}

func TestHubRejectsAlreadyRemoteEventIDs(t *testing.T) {
	for _, kind := range []string{"task_status_changed", "chat_response_done"} {
		for _, field := range []string{"project_id", "task_id", "exec_id"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				c := &Conn{serverID: server, hub: &Hub{
					OnTask: func(events.TaskEvent) { t.Fatal("imported a relayed task event") },
					OnChat: func(events.ChatEvent) { t.Fatal("imported a relayed chat event") },
				}}
				data, err := json.Marshal(map[string]string{"type": kind, field: remoteid.ID("fedcba9876543210", "entity")})
				if err != nil {
					t.Fatal(err)
				}
				c.dispatch(Frame{ID: liveSubscriptionID, Event: kind, Data: data})
			})
		}
	}
}
