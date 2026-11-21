package service

import (
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/toolschema"
	"strings"
)

func projectComputerToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	return []llmcontracts.RuntimeToolDefinition{
		{Name: "computer",
			Description: "Use this task's persistent Linux desktop. Actions: get_desktop_state, click, double_click, right_click, drag, scroll, press_key, hotkey, type_text, wait. Inspect the returned screenshot after each action. Input does not prove the intended operation completed. Private sign-in and help requests block agent access until resolved. This desktop is separate from your coding worktree.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"action": &toolschema.Schema{
						Type: "string",
						Enum: []any{"get_desktop_state", "get_window_state", "get_screen_size", "get_cursor_position", "list_windows", "list_apps", "click", "double_click", "right_click", "drag", "scroll", "press_key", "hotkey", "type_text", "wait"},
					},
					"values": &toolschema.Schema{
						Type: "object",
						Properties: map[string]*toolschema.Schema{
							"x": &toolschema.Schema{
								Type: "integer",
							},
							"y": &toolschema.Schema{
								Type: "integer",
							},
							"from_x": &toolschema.Schema{
								Type: "integer",
							},
							"from_y": &toolschema.Schema{
								Type: "integer",
							},
							"to_x": &toolschema.Schema{
								Type: "integer",
							},
							"to_y": &toolschema.Schema{
								Type: "integer",
							},
							"count": &toolschema.Schema{
								Type: "integer",
							},
							"text": &toolschema.Schema{
								Type: "string",
							},
							"key": &toolschema.Schema{
								Type: "string",
							},
							"direction": &toolschema.Schema{
								Type: "string",
								Enum: []any{"up", "down", "left", "right"},
							},
							"amount": &toolschema.Schema{
								Type:    "integer",
								Minimum: toolschema.Number("1"),
								Maximum: toolschema.Number("20"),
							},
							"duration_ms": &toolschema.Schema{
								Type:    "integer",
								Minimum: toolschema.Number("0"),
								Maximum: toolschema.Number("10000"),
							},
							"button": &toolschema.Schema{
								Type: "string",
								Enum: []any{"left", "middle", "right"},
							},
							"modifiers": &toolschema.Schema{
								Type: "array",
								Items: &toolschema.Schema{
									Type: "string",
								},
							},
						},
						Required:             []string{},
						AdditionalProperties: toolschema.Pointer(false),
					},
				},
				Required:             []string{"action"},
				AdditionalProperties: toolschema.Pointer(false),
			}},
		computerProcessDefinition("computer_shell", "shell"),
		{Name: "computer_files",
			Description: "Read, write, edit, list or search files inside the project computer. Relative paths use /home/ubuntu/Files, separate from your coding worktree.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"operation": &toolschema.Schema{
						Type: "string",
						Enum: []any{"read_file", "write_file", "edit_file", "list_files", "grep_search"},
					},
					"input": &toolschema.Schema{
						Type: "object",
						Properties: map[string]*toolschema.Schema{
							"file_path": &toolschema.Schema{
								Type: "string",
							},
							"path": &toolschema.Schema{
								Type: "string",
							},
							"content": &toolschema.Schema{
								Type: "string",
							},
							"old_string": &toolschema.Schema{
								Type: "string",
							},
							"new_string": &toolschema.Schema{
								Type: "string",
							},
							"pattern": &toolschema.Schema{
								Type: "string",
							},
							"include": &toolschema.Schema{
								Type: "string",
							},
							"replace_all": &toolschema.Schema{
								Type: "boolean",
							},
							"offset": &toolschema.Schema{
								Type: "integer",
							},
							"limit": &toolschema.Schema{
								Type: "integer",
							},
							"recursive": &toolschema.Schema{
								Type: "boolean",
							},
						},
						Required:             []string{},
						AdditionalProperties: toolschema.Pointer(false),
					},
				},
				Required:             []string{"operation", "input"},
				AdditionalProperties: toolschema.Pointer(false),
			}},
		{Name: "release_computer",
			Description: "Release your project desktop reservation when finished so another task can use it.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type:                 "object",
				Properties:           map[string]*toolschema.Schema{},
				Required:             []string{},
				AdditionalProperties: toolschema.Pointer(false),
			}},
		{Name: "computer_transfer",
			Description: "Copy a file between this task's worktree and its computer Files directory. Relative paths only, up to 25 MB. Existing files are never overwritten.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"direction": &toolschema.Schema{
						Type: "string",
						Enum: []any{"to_computer", "from_computer"},
					},
					"local_path": &toolschema.Schema{
						Type: "string",
					},
					"computer_path": &toolschema.Schema{
						Type: "string",
					},
				},
				Required:             []string{"direction", "local_path", "computer_path"},
				AdditionalProperties: toolschema.Pointer(false),
			}},
		{Name: "project_browser",
			Description: "Use this task's signed-in browser. List/open/select/close tabs or navigate/read/interact with a tab. Supply tab_id to target a tab; otherwise the selected tab is used. Use find with a structured locator and optional frames to inspect matching elements. For element actions use a locator (optionally within frames), or read first and send snapshot plus ref. click_navigation clicks while waiting for navigation, optionally to url. wait_element uses state; wait_url uses url; wait_load uses state. type replaces text; append/type_sequential preserve existing text. set_checked uses checked. select accepts value or options with value/label/index. Re-read after changes. upload targets a file input or chooser button and takes files. download clicks a download control and waits; download_media saves an HTTP media/link URL. Both return computer_path for computer_attachment publishing. Password and verification-code entry require a private login handoff. Never read cookie values or browser storage.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"action": &toolschema.Schema{
						Type: "string",
						Enum: projectBrowserActions(),
					},
					"url": &toolschema.Schema{
						Type: "string",
					},
					"tab_id":        &toolschema.Schema{Type: "string"},
					"locator":       projectBrowserLocatorSchema(0),
					"frames":        &toolschema.Schema{Type: "array", Items: &toolschema.Schema{Type: "string"}, Description: "Outer-to-inner iframe selectors."},
					"offset":        &toolschema.Schema{Type: "integer", Minimum: toolschema.Number("0")},
					"limit":         &toolschema.Schema{Type: "integer", Minimum: toolschema.Number("1"), Maximum: toolschema.Number("500")},
					"property":      &toolschema.Schema{Type: "string", Enum: []any{"count", "text", "attribute", "visible", "enabled"}},
					"attribute":     &toolschema.Schema{Type: "string", Description: "Public DOM attribute; value, style, event handlers and arbitrary data attributes are not readable."},
					"text_offset":   &toolschema.Schema{Type: "integer", Minimum: toolschema.Number("0")},
					"button":        &toolschema.Schema{Type: "string", Enum: []any{"left", "middle", "right"}},
					"modifiers":     &toolschema.Schema{Type: "array", Items: &toolschema.Schema{Type: "string", Enum: []any{"Alt", "Control", "ControlOrMeta", "Meta", "Shift"}}},
					"force":         &toolschema.Schema{Type: "boolean"},
					"checked":       &toolschema.Schema{Type: "boolean"},
					"options":       &toolschema.Schema{Type: "array", Items: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"value": {Type: "string"}, "label": {Type: "string"}, "index": {Type: "integer", Minimum: toolschema.Number("0")}}, AdditionalProperties: toolschema.Pointer(false)}},
					"timeout_ms":    &toolschema.Schema{Type: "integer", Minimum: toolschema.Number("1"), Maximum: toolschema.Number("20000")},
					"state":         &toolschema.Schema{Type: "string", Enum: []any{"visible", "hidden", "attached", "detached", "enabled", "disabled", "load", "domcontentloaded", "networkidle"}},
					"full_page":     &toolschema.Schema{Type: "boolean"},
					"clip":          &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"x": {Type: "number", Minimum: toolschema.Number("0")}, "y": {Type: "number", Minimum: toolschema.Number("0")}, "width": {Type: "number"}, "height": {Type: "number"}}, Required: []string{"x", "y", "width", "height"}, AdditionalProperties: toolschema.Pointer(false)},
					"dialog_action": &toolschema.Schema{Type: "string", Enum: []any{"inspect", "accept", "dismiss"}},
					"files":         &toolschema.Schema{Type: "array", Items: &toolschema.Schema{Type: "string"}, Description: "Upload paths relative to computer Files (or absolute paths within it), maximum 25 MB total."},
					"snapshot":      &toolschema.Schema{Type: "string"},
					"ref":           &toolschema.Schema{Type: "string"},
					"text": &toolschema.Schema{
						Type: "string",
					},
					"value": &toolschema.Schema{
						Type: "string",
					},
					"key": &toolschema.Schema{
						Type: "string",
					},
					"amount": &toolschema.Schema{
						Type: "number",
					},
				},
				Required:             []string{"action"},
				AdditionalProperties: toolschema.Pointer(false),
			}},
		computerProcessDefinition("computer_job", "process"),
		{Name: "computer_chrome_grant", Description: "List approved Chrome grants or apply an unexpired grant to this task browser. Task grants are only usable by their task; project grants are usable within this project. Cookie values are never returned.", Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"action": {Type: "string", Enum: []any{"list", "use"}}, "grant_id": {Type: "string"}}, Required: []string{"action"}, AdditionalProperties: toolschema.Pointer(false)}},
		{Name: "computer_attachment", Description: "List task thread attachments, copy one to computer Files, or publish a computer file as a downloadable artifact in the conversation and Files gallery. Use from_computer to share or download a computer file; include the returned URL in your answer. Max 25 MB; existing computer files are preserved.", Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"action": {Type: "string", Enum: []any{"list", "to_computer", "from_computer"}}, "attachment_kind": {Type: "string", Enum: []any{"task", "thread"}}, "attachment_id": {Type: "string"}, "computer_path": {Type: "string"}}, Required: []string{"action"}, AdditionalProperties: toolschema.Pointer(false)}},
		computerHandoffDefinition("request_browser_login", "Request private sign-in. Specify the protected destination and visible text proving access. End this turn; the user completion verifies access and resumes this task.", true),
		computerHandoffDefinition("request_chrome_login", "Request user-approved Chrome cookie import on local macOS or Linux. Specify the protected destination and expected page text. End this turn; completion resumes this task.", true),
		computerHandoffDefinition("request_computer_help", "Request human help in the task desktop, such as a CAPTCHA or browser prompt. Creates an Inbox request. End this turn; completing or declining it resumes this task.", false),
	}
}
func computerHandoffDefinition(name, description string, login bool) llmcontracts.RuntimeToolDefinition {
	props := map[string]*toolschema.Schema{"reason": {Type: "string", MaxLength: toolschema.Pointer(2000)}}
	required := []string{"reason"}
	if login {
		props["url"] = &toolschema.Schema{Type: "string"}
		props["expected_text"] = &toolschema.Schema{Type: "string", Description: "Specific visible text on the protected page proving access, not generic login text.", MaxLength: toolschema.Pointer(500)}
		required = append(required, "url", "expected_text")
	}
	return llmcontracts.RuntimeToolDefinition{Name: name, Description: description, Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: props, Required: required, AdditionalProperties: toolschema.Pointer(false)}}
}

// Explicit computer tools share the standard schemas, including future changes.
func computerProcessDefinition(name, standard string) llmcontracts.RuntimeToolDefinition {
	for _, d := range agenttools.Definitions() {
		if d.Name == standard {
			description := strings.Split(strings.Split(d.Description, " Selected shell:")[0], " Shell unavailable:")[0]
			if name == "computer_shell" {
				description = strings.ReplaceAll(description, "process", "computer_job")
			}
			if name == "computer_job" {
				description = strings.ReplaceAll(description, "shell", "computer_shell")
			}
			return llmcontracts.RuntimeToolDefinition{Name: name, Description: "Always runs in this task computer under Files, using isolated Linux bash. " + description, Schema: d.Schema, Access: llmcontracts.RuntimeToolAccessWrite}
		}
	}
	panic("unknown standard process tool: " + standard)
}

func projectBrowserActions() []any {
	return []any{"navigate", "read", "click", "type", "select", "press_key", "scroll", "back", "forward", "tabs", "open_tab", "select_tab", "close_tab", "reload", "find", "inspect", "double_click", "append", "type_sequential", "set_checked", "wait_element", "wait_url", "wait_load", "click_navigation", "screenshot", "dialog", "upload", "download", "download_media", "logs"}
}

func validProjectBrowserAction(action string) bool {
	for _, allowed := range projectBrowserActions() {
		if action == allowed {
			return true
		}
	}
	return false
}

func projectBrowserLocatorSchema(depth int) *toolschema.Schema {
	p := map[string]*toolschema.Schema{}
	for _, key := range []string{"selector", "role", "name", "text", "label", "placeholder", "test_id", "has_text", "has_not_text"} {
		p[key] = &toolschema.Schema{Type: "string"}
	}
	p["exact"] = &toolschema.Schema{Type: "boolean"}
	p["visible"] = &toolschema.Schema{Type: "boolean"}
	p["index"] = &toolschema.Schema{Type: "integer", Minimum: toolschema.Number("0")}
	if depth < 1 {
		for _, key := range []string{"and", "or", "has", "has_not", "child"} {
			p[key] = projectBrowserLocatorSchema(depth + 1)
		}
	}
	return &toolschema.Schema{Type: "object", Description: "Exactly one of selector, role, text, label, placeholder or test_id; role can use name. Optional filters, zero-based index, and nested combinations.", Properties: p, AdditionalProperties: toolschema.Pointer(false)}
}
