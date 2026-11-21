package remote

import "reflect"

// Reads returning bare IDs need an explicit result schema: their values must
// not be inferred from string contents. Only result zero is an ID collection.
var bareIDResults = map[string]bool{
	"Alerts.ListOwnerProjectIDs":                            true,
	"Automations.ListAutomationInvocationNodeIDs":           true,
	"Executions.GetExecutionTaskID":                         true,
	"LLMConfigs.GetDefaultID":                               true,
	"Projects.GetDefaultAgentConfigID":                      true,
	"Tasks.ListRunningChatTaskIDs":                          true,
	"ThreadInputs.ListRecoverableQueuedChatProjectIDsAfter": true,
	"ThreadInputs.ListRecoverableQueuedTaskIDsAfter":        true,
}

// These maps are keyed by entity ID. Values are traversed independently, so
// free text in a map value is not treated as another ID.
var idKeyResults = map[string]bool{
	"Attachments.ListByExecutionIDs":                       true,
	"ChatAttachments.ListByExecutionIDs":                   true,
	"Automations.LiveEdgeCounts":                           true,
	"Automations.LiveNodeCounts":                           true,
	"Automations.PortfolioOperationalCounts":               true,
	"Automations.PortfolioOperationalCountsForAutomations": true,
	"Executions.ReplayMessagesByExecutionIDs":              true,
	"Executions.ReplayRowsByExecutionIDs":                  true,
	"Executions.ReplayContinuationRowsByExecutionIDs":      true,
	"LLMConfigs.GetByIDs":                                  true,
	"Tasks.CountPendingByProject":                          true,
	"Tasks.FilterNonChatTaskIDs":                           true,
}

func tagReadResult(server, store, method string, index int, v reflect.Value) {
	tag := tagger(server)
	key := store + "." + method
	name := ""
	if index == 0 && bareIDResults[key] {
		name = "ID"
	}
	walk(v, tag, nil, name, 0)
	if index != 0 || !idKeyResults[key] || v.Kind() != reflect.Map || v.IsNil() {
		return
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	iter := v.MapRange()
	for iter.Next() {
		k := reflect.New(v.Type().Key()).Elem()
		k.Set(iter.Key())
		walk(k, tag, nil, "ID", 0)
		out.SetMapIndex(k, iter.Value())
	}
	v.Set(out)
}
