package handler

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestPreparedSteeringBatchHelpers(t *testing.T) {
	batch := preparedSteeringBatch{inputs: []models.ThreadInput{{ID: "one"}, {ID: "two"}, {ID: "three"}}}
	if got := preparedSteeringInputIDs(batch); len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Fatalf("preparedSteeringInputIDs = %#v", got)
	}
	kept := removePreparedSteeringInputs(batch, preparedSteeringBatch{inputs: []models.ThreadInput{{ID: "two"}}})
	if got := preparedSteeringInputIDs(kept); len(got) != 2 || got[0] != "one" || got[1] != "three" {
		t.Fatalf("removePreparedSteeringInputs = %#v", got)
	}
	if got := removePreparedSteeringInputs(preparedSteeringBatch{}, kept); got.count() != 0 {
		t.Fatalf("empty batch removal count = %d", got.count())
	}
	if got := removePreparedSteeringInputs(kept, preparedSteeringBatch{}); got.count() != 2 {
		t.Fatalf("empty removal count = %d", got.count())
	}
}

func TestParseCompletionOptions(t *testing.T) {
	messageID, reply := parseCompletionOptions(
		service.ChannelReplyContext{Source: models.TaskOriginSlack, SlackChannelID: "C1"},
		17,
	)
	if messageID != 17 || reply.Source != models.TaskOriginSlack || reply.SlackChannelID != "C1" {
		t.Fatalf("parseCompletionOptions = %d %#v", messageID, reply)
	}
}

func TestHasOtherEditFields(t *testing.T) {
	if hasOtherEditFields(service.TaskEditRequest{}) {
		t.Fatal("empty edit request should not have edit fields")
	}
	if !hasOtherEditFields(service.TaskEditRequest{Title: "New title"}) {
		t.Fatal("title should count as an edit field")
	}
	if !hasOtherEditFields(service.TaskEditRequest{Attachments: []string{"note.txt"}}) {
		t.Fatal("attachments should count as edit fields")
	}
	if !hasOtherEditFields(service.TaskEditRequest{AgentDefinitionID: "agent-def-1"}) {
		t.Fatal("agent_definition_id should count as an edit field")
	}
	if !hasOtherEditFields(service.TaskEditRequest{Agent: "Docs Reviewer"}) {
		t.Fatal("agent should count as an edit field")
	}
	if !hasOtherEditFields(service.TaskEditRequest{ClearAgentDefinition: true}) {
		t.Fatal("clear_agent_definition should count as an edit field")
	}
	chain := models.ChainConfiguration{}
	if !hasOtherEditFields(service.TaskEditRequest{Chain: &chain}) {
		t.Fatal("chain should count as an edit field")
	}
}
