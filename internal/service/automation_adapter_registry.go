package service

import (
	"fmt"
	"sort"
)

const (
	AutomationAdapterCustom       = "custom"
	AutomationAdapterNativeSDLC   = "native_sdlc"
	AutomationAdapterGitHubSDLC   = "github_sdlc"
	AutomationAdapterVisionDriver = "vision_driver"
)

type AutomationAdapterNode struct {
	Key              string
	Name             string
	Type             string
	Role             string
	AllowedResources map[string]bool
	X                float64
	Y                float64
}

type AutomationAdapterEdge struct {
	Key       string
	From      string
	To        string
	Label     string
	Condition string
}

type AutomationAdapter struct {
	Key              string
	AutomationType   string
	DefaultName      string
	Description      string
	TemplateRevision int
	DynamicTopology  bool
	Nodes            []AutomationAdapterNode
	Edges            []AutomationAdapterEdge
}

type AutomationAdapterRegistry struct{ adapters map[string]AutomationAdapter }

// CurrentAutomationTemplateRevision is bumped when a maintained template changes
// and existing Automations should offer destructive replacement with that template.
func CurrentAutomationTemplateRevision(adapterKey string) int {
	switch adapterKey {
	case AutomationAdapterNativeSDLC:
		return 12
	case AutomationAdapterGitHubSDLC:
		return 15
	default:
		return 0
	}
}

func NewAutomationAdapterRegistry() *AutomationAdapterRegistry {
	registry := &AutomationAdapterRegistry{adapters: make(map[string]AutomationAdapter)}
	for _, adapter := range []AutomationAdapter{customAutomationAdapter(), nativeSDLCAdapter(), githubSDLCAdapter(), visionDriverAdapter()} {
		adapter.TemplateRevision = CurrentAutomationTemplateRevision(adapter.Key)
		registry.adapters[adapter.Key] = adapter
	}
	return registry
}

func (r *AutomationAdapterRegistry) Get(key string) (AutomationAdapter, bool) {
	adapter, ok := r.adapters[key]
	return adapter, ok
}

func (r *AutomationAdapterRegistry) Keys() []string {
	keys := make([]string, 0, len(r.adapters))
	for key := range r.adapters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a AutomationAdapter) ValidateBinding(nodeKey, resourceType string) error {
	for _, node := range a.Nodes {
		if node.Key != nodeKey {
			continue
		}
		if !node.AllowedResources[resourceType] {
			return fmt.Errorf("adapter %q node %q does not accept %q resources", a.Key, nodeKey, resourceType)
		}
		return nil
	}
	return fmt.Errorf("adapter %q has no node %q", a.Key, nodeKey)
}

func resourceTypes(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func customAutomationAdapter() AutomationAdapter {
	return AutomationAdapter{
		Key: AutomationAdapterCustom, AutomationType: "custom", DefaultName: "Custom Automation",
		Description: "Connect configurable OpenVibely capabilities into a runnable automation.", DynamicTopology: true,
	}
}

func nativeSDLCAdapter() AutomationAdapter {
	return AutomationAdapter{
		Key: AutomationAdapterNativeSDLC, AutomationType: "native_sdlc", DefaultName: "Native SDLC",
		Description: "Find vision suggestions, bugs, optimizations, and redundancies; wait for native approval; and create visible implementation work.",
		Nodes: []AutomationAdapterNode{
			{Key: "vision_suggestions", Name: "Vision Suggestions", Type: "trigger", Role: "offering_manager", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 0},
			{Key: "bug_finder", Name: "Bug Finder", Type: "trigger", Role: "bug_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 120},
			{Key: "optimization_finder", Name: "Optimization Finder", Type: "trigger", Role: "optimization_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 240},
			{Key: "redundancy_finder", Name: "Redundancy Finder", Type: "trigger", Role: "redundancy_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 360},
			{Key: "notification", Name: "Pending Notification", Type: "action", Role: "create_notification", AllowedResources: resourceTypes(), X: 220, Y: 180},
			{Key: "approval", Name: "Human Approval", Type: "human_gate", Role: "native_approval", AllowedResources: resourceTypes(), X: 440, Y: 180},
			{Key: "inbox", Name: "Approved Inbox", Type: "trigger", Role: "native_inbox", AllowedResources: resourceTypes("task", "schedule"), X: 660, Y: 270},
			{Key: "implementation", Name: "Implementation", Type: "agent_task", Role: "implementation", AllowedResources: resourceTypes(), X: 880, Y: 270},
			{Key: "rejected", Name: "Rejected", Type: "outcome", Role: "rejected", AllowedResources: resourceTypes(), X: 660, Y: 90},
			{Key: "completed", Name: "Completed", Type: "outcome", Role: "completed", AllowedResources: resourceTypes(), X: 1100, Y: 270},
		},
		Edges: []AutomationAdapterEdge{
			{Key: "vision_to_notification", From: "vision_suggestions", To: "notification"},
			{Key: "bug_to_notification", From: "bug_finder", To: "notification"},
			{Key: "optimization_to_notification", From: "optimization_finder", To: "notification"},
			{Key: "redundancy_to_notification", From: "redundancy_finder", To: "notification"},
			{Key: "notification_to_approval", From: "notification", To: "approval"},
			{Key: "approval_to_rejected", From: "approval", To: "rejected", Label: "rejected", Condition: `{"state":"rejected"}`},
			{Key: "approval_to_inbox", From: "approval", To: "inbox", Label: "approved", Condition: `{"state":"approved"}`},
			{Key: "inbox_to_implementation", From: "inbox", To: "implementation"},
			{Key: "implementation_to_completed", From: "implementation", To: "completed"},
		},
	}
}

func visionDriverAdapter() AutomationAdapter {
	return AutomationAdapter{
		Key: AutomationAdapterVisionDriver, AutomationType: "vision_driver", DefaultName: "Vision Driver",
		Description: "Review project vision, propose improvements for approval, and create visible implementation work.",
		Nodes: []AutomationAdapterNode{
			{Key: "vision_driver", Name: "Vision Driver", Type: "trigger", Role: "vision_driver", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 0},
			{Key: "notification", Name: "Improvement Proposal", Type: "action", Role: "create_notification", AllowedResources: resourceTypes(), X: 220, Y: 0},
			{Key: "approval", Name: "Human Approval", Type: "human_gate", Role: "native_approval", AllowedResources: resourceTypes(), X: 440, Y: 0},
			{Key: "implementation", Name: "Implementation", Type: "agent_task", Role: "implementation", AllowedResources: resourceTypes(), X: 660, Y: 0},
			{Key: "completed", Name: "Completed", Type: "outcome", Role: "completed", AllowedResources: resourceTypes(), X: 880, Y: 0},
		},
		Edges: []AutomationAdapterEdge{
			{Key: "driver_to_notification", From: "vision_driver", To: "notification"},
			{Key: "notification_to_approval", From: "notification", To: "approval"},
			{Key: "approval_to_implementation", From: "approval", To: "implementation", Label: "approved", Condition: `{"state":"approved"}`},
			{Key: "implementation_to_completed", From: "implementation", To: "completed"},
		},
	}
}

func githubSDLCAdapter() AutomationAdapter {
	return AutomationAdapter{
		Key: AutomationAdapterGitHubSDLC, AutomationType: "github_sdlc", DefaultName: "GitHub SDLC",
		Description: "Create GitHub suggestions, use assignment as approval, implement accepted issues, and open pull requests for review.",
		Nodes: []AutomationAdapterNode{
			{Key: "vision_suggestions", Name: "Vision Suggestions", Type: "trigger", Role: "offering_manager", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 0},
			{Key: "bug_finder", Name: "Bug Finder", Type: "trigger", Role: "bug_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 120},
			{Key: "optimization_finder", Name: "Optimization Finder", Type: "trigger", Role: "optimization_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 240},
			{Key: "redundancy_finder", Name: "Redundancy Finder", Type: "trigger", Role: "redundancy_finder", AllowedResources: resourceTypes("task", "schedule"), X: 0, Y: 360},
			{Key: "issue", Name: "GitHub Issue", Type: "action", Role: "create_github_issue", AllowedResources: resourceTypes(), X: 220, Y: 180},
			{Key: "assignment", Name: "Human Assignment", Type: "human_gate", Role: "github_assignment", AllowedResources: resourceTypes(), X: 440, Y: 180},
			{Key: "dev_inbox", Name: "Dev Inbox", Type: "trigger", Role: "github_inbox", AllowedResources: resourceTypes("task", "schedule"), X: 660, Y: 270},
			{Key: "implementation", Name: "Implementation", Type: "agent_task", Role: "implementation", AllowedResources: resourceTypes(), X: 880, Y: 270},
			{Key: "open_pr", Name: "Open Pull Request", Type: "action", Role: "open_pull_request", AllowedResources: resourceTypes(), X: 1100, Y: 270},
			{Key: "review", Name: "Human Review", Type: "human_gate", Role: "pull_request_review", AllowedResources: resourceTypes(), X: 1320, Y: 270},
			{Key: "completed", Name: "Completed", Type: "outcome", Role: "completed", AllowedResources: resourceTypes(), X: 1540, Y: 270},
		},
		Edges: []AutomationAdapterEdge{
			{Key: "vision_to_issue", From: "vision_suggestions", To: "issue"},
			{Key: "bug_to_issue", From: "bug_finder", To: "issue"},
			{Key: "optimization_to_issue", From: "optimization_finder", To: "issue"},
			{Key: "redundancy_to_issue", From: "redundancy_finder", To: "issue"},
			{Key: "issue_to_assignment", From: "issue", To: "assignment"},
			{Key: "assignment_to_inbox", From: "assignment", To: "dev_inbox", Label: "assigned", Condition: `{"state":"assigned"}`},
			{Key: "inbox_to_implementation", From: "dev_inbox", To: "implementation"},
			{Key: "implementation_to_pr", From: "implementation", To: "open_pr"},
			{Key: "pr_to_review", From: "open_pr", To: "review"},
			{Key: "review_to_completed", From: "review", To: "completed"},
		},
	}
}
