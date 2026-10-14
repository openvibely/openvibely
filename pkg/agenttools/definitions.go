package agenttools

import (
	"fmt"

	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

type Definition struct {
	Name        string
	Description string
	Schema      *toolschema.Schema
}

// Definitions returns a fresh slice around shared, read-only schemas.
func Definitions() []Definition {
	out := append([]Definition(nil), definitions...)
	for i := range out {
		if out[i].Name == "shell" {
			if shell, err := executionenv.Resolve(); err == nil {
				out[i].Description += " Selected shell: " + shell.DisplayName() + ". " + shell.Instruction()
			} else {
				out[i].Description += " Shell unavailable: " + err.Error()
			}
		}
	}
	return out
}

var definitions = []Definition{readFileTool, writeFileTool, editFileTool, bashTool, interactProcessTool, listFilesTool, grepSearchTool}

var readFileTool = Definition{
	Name:        "read_file",
	Description: "Read the contents of a file. Returns the file content as text. For large files, use offset and limit to read specific portions.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to read. Relative paths are resolved against the working directory.",
			},
			"offset": {
				Type:        "integer",
				Description: "Line number to start reading from (0-based). Default: 0",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of lines to read. Default: 5000, Max: 10000",
			},
		},
		Required:             []string{"file_path"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var writeFileTool = Definition{
	Name:        "write_file",
	Description: "Write content to a file. Creates the file and any parent directories if they don't exist. Overwrites existing files.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to write. Relative paths are resolved against the working directory.",
			},
			"content": {
				Type:        "string",
				Description: "The content to write to the file.",
			},
		},
		Required:             []string{"file_path", "content"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var editFileTool = Definition{
	Name:        "edit_file",
	Description: "Edit a file by replacing old_string with new_string. Tries exact match first, then whitespace-tolerant line matching (Codex-style) if exact match fails. Use this for surgical edits rather than rewriting entire files.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to edit. Relative paths are resolved against the working directory.",
			},
			"old_string": {
				Type:        "string",
				Description: "The exact string to find and replace. Must be unique within the file.",
			},
			"new_string": {
				Type:        "string",
				Description: "The replacement string.",
			},
			"replace_all": {
				Type:        "boolean",
				Description: "If true, replace all occurrences. Default: false",
			},
		},
		Required:             []string{"file_path", "old_string", "new_string"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var bashTool = Definition{
	Name:        "shell",
	Description: fmt.Sprintf("Execute a shell command and return its stdout and stderr. The command runs in the working directory. Use this for running tests, builds, git commands, and other shell operations. Commands have a default total runtime timeout of %d seconds. If still running after yield_time_ms, returns a session_id; use interact_process to read output, send stdin input, or stop it. Sessions use pipes, not a terminal (no TTY).", DefaultBashTimeoutSeconds),
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"command": {
				Type:        "string",
				Description: "The command to execute using the selected shell.",
			},
			"yield_time_ms": {
				Type:        "integer",
				Description: "Initial wait in milliseconds before returning a running session. Default: 10000. Clamped to 250..30000. Does not change the total runtime timeout.",
			},
			"timeout": {
				Type:        "integer",
				Description: fmt.Sprintf("Timeout in seconds. Default: %d. Max: %d.", DefaultBashTimeoutSeconds, MaxBashTimeoutSeconds),
			},
		},
		Required:             []string{"command"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var listFilesTool = Definition{
	Name:        "list_files",
	Description: "List files and directories at a given path. Returns file names with '/' suffix for directories. Useful for exploring project structure.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"path": {
				Type:        "string",
				Description: "Directory path to list. Relative paths are resolved against the working directory. Default: current working directory.",
			},
			"recursive": {
				Type:        "boolean",
				Description: "If true, list files recursively (max depth 4, max 500 entries). Default: false",
			},
			"pattern": {
				Type:        "string",
				Description: "Glob pattern to filter results (e.g. '*.go', '*.ts'). Only applies to filenames, not paths.",
			},
		},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var grepSearchTool = Definition{
	Name:        "grep_search",
	Description: "Search file contents using a regular expression pattern. Returns matching lines with file paths and line numbers. Useful for finding function definitions, imports, references, etc.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"pattern": {
				Type:        "string",
				Description: "Regular expression pattern to search for.",
			},
			"path": {
				Type:        "string",
				Description: "Directory or file to search in. Default: working directory.",
			},
			"include": {
				Type:        "string",
				Description: "Glob pattern to filter files (e.g. '*.go', '*.ts'). Default: all files.",
			},
		},
		Required:             []string{"pattern"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var interactProcessTool = Definition{
	Name:        "interact_process",
	Description: "Interact with a running shell session returned by shell. With no input, wait for new stdout/stderr or completion. Optionally send input (include a newline to submit a line), close stdin, or terminate the process. Does not start a new command. Returns unread output, running status and session_id, or final exit_code. Sessions are restricted to their original working directory, expire five minutes after completion, and are removed after final output is read. Output is bounded; truncation is reported. Interactive programs requiring a TTY are unsupported.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"session_id":    {Type: "string", Description: "The session_id returned by shell."},
			"input":         {Type: "string", Description: "Optional stdin text, up to 65536 bytes. Empty or omitted means poll only. Include a newline when answering a prompt."},
			"yield_time_ms": {Type: "integer", Description: "Maximum wait for output or completion, in milliseconds. Polling without input or control actions: default 5000, clamped to 5000..300000. Sending input, closing stdin, or terminating: default 250, clamped to 250..30000. Does not extend the command's total runtime timeout."},
			"close_stdin":   {Type: "boolean", Description: "Close stdin after sending any input, delivering EOF. Default: false."},
			"terminate":     {Type: "boolean", Description: "Stop the running process. Cannot be combined with input or close_stdin. Default: false."},
		},
		Required:             []string{"session_id"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}
