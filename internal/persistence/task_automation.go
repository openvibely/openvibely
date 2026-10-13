package persistence

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
)

var ErrAutomationChainChildBusy = errors.New("automation chained task is already running")

func AutomationCompilerTaskCreatedVia(automationID, nodeKey string) string {
	return "automation:" + automationID + ":" + nodeKey
}

func IsAutomationTaskCreatedVia(createdVia string) bool {
	parts := strings.Split(strings.TrimSpace(createdVia), ":")
	return len(parts) >= 3 && parts[0] == "automation" && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(parts[2]) != ""
}

// QueuedAutomationDispatchClaim contains the execution and authoritative Task
// admitted by a queued Automation dispatch.
type QueuedAutomationDispatchClaim struct {
	Execution models.Execution
	Task      models.Task
}
