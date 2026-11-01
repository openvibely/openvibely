package service

import (
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

func projectComputerToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	return []llmcontracts.RuntimeToolDefinition{
		{Name: "computer",
			Description: "Use this task's persistent Linux desktop. Actions: get_desktop_state, click, double_click, right_click, drag, scroll, press_key, hotkey, type_text, wait. Inspect the returned screenshot after each action. Input does not prove the intended operation completed. User takeover blocks agent access. This desktop is separate from your coding worktree.",
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
		{Name: "computer_shell",
			Description: "Execute a Linux command inside the project computer, separate from the coding worktree. Files and browser profiles persist. Output is visible live in the task panel. Set background=true for a job that survives the tool call; read or cancel it with computer_job. Foreground timeout is 180 seconds; background timeout is 3600 seconds.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"background": &toolschema.Schema{Type: "boolean"},
					"command": &toolschema.Schema{
						Type: "string",
					},
					"working_directory": &toolschema.Schema{
						Type:        "string",
						Description: "Path inside the task desktop home; defaults to Files.",
					},
					"timeout_seconds": &toolschema.Schema{
						Type:    "integer",
						Minimum: toolschema.Number("1"),
						Maximum: toolschema.Number("3600"),
					},
				},
				Required:             []string{"command"},
				AdditionalProperties: toolschema.Pointer(false),
			}},
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
			Description: "Use this task's signed-in browser: navigate, read, click, type, select, press_key, scroll, back, forward. Read first. For click, type, select and press_key, send the snapshot token from the latest result; targeted actions also require its control ref. Re-read after changes. Password and verification-code entry require a private login handoff. Never read cookie values or browser storage.",
			Access:      llmcontracts.RuntimeToolAccessWrite,
			Schema: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"action": &toolschema.Schema{
						Type: "string",
						Enum: []any{"navigate", "read", "click", "type", "select", "press_key", "scroll", "back", "forward"},
					},
					"url": &toolschema.Schema{
						Type: "string",
					},
					"snapshot": &toolschema.Schema{Type: "string"},
					"ref":      &toolschema.Schema{Type: "string"},
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
		{Name: "computer_job", Description: "List, read current output, or cancel a job in this task computer. Job IDs only exist in this desktop and are lost when it restarts.", Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"action": {Type: "string", Enum: []any{"list", "read", "cancel"}}, "job_id": {Type: "string"}}, Required: []string{"action"}, AdditionalProperties: toolschema.Pointer(false)}},
		{Name: "computer_chrome_grant", Description: "List approved Chrome grants or apply an unexpired grant to this task browser. Task grants are only usable by their task; project grants are usable within this project. Cookie values are never returned.", Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"action": {Type: "string", Enum: []any{"list", "use"}}, "grant_id": {Type: "string"}}, Required: []string{"action"}, AdditionalProperties: toolschema.Pointer(false)}},
		{Name: "computer_attachment", Description: "List task thread attachments, copy one to computer Files, or attach a computer file to this task thread. Max 25 MB; existing computer files are preserved.", Access: llmcontracts.RuntimeToolAccessWrite, Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"action": {Type: "string", Enum: []any{"list", "to_computer", "from_computer"}}, "attachment_kind": {Type: "string", Enum: []any{"task", "thread"}}, "attachment_id": {Type: "string"}, "computer_path": {Type: "string"}}, Required: []string{"action"}, AdditionalProperties: toolschema.Pointer(false)}},
		computerHandoffDefinition("request_browser_login", "Request private sign-in. Specify the protected destination and visible text proving access. End this turn; the user completion verifies access and resumes this task.", true),
		computerHandoffDefinition("request_chrome_login", "Request user-approved Chrome cookie import on local macOS. Specify the protected destination and expected page text. End this turn; completion resumes this task.", true),
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
