package anthropicclient

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall records a single tool invocation and its result.
type ToolCall struct {
	Name   string                 `json:"name"`
	Input  map[string]interface{} `json:"input"`
	Output string                 `json:"output"`
	Error  bool                   `json:"error"`
}

// DefaultTools returns the standard set of tool definitions for agentic use.
func DefaultTools() []ToolDefinition {
	return []ToolDefinition{
		readFileTool(),
		writeFileTool(),
		editFileTool(),
		bashTool(),
		listFilesTool(),
		grepSearchTool(),
	}
}

func filterToolDefinitions(tools []ToolDefinition, filter func(string) bool) []ToolDefinition {
	if filter == nil || len(tools) == 0 {
		return tools
	}
	out := tools[:0]
	for _, tool := range tools {
		if filter(strings.TrimSpace(tool.Name)) {
			out = append(out, tool)
		}
	}
	return out
}

func readFileTool() ToolDefinition {
	return ToolDefinition{
		Name:        "read_file",
		Description: "Read the contents of a file. Returns the file content as text. For large files, use offset and limit to read specific portions.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "The path to the file to read. Relative paths are resolved against the working directory."},
				"offset": {"type": "integer", "description": "Line number to start reading from (0-based). Default: 0"},
				"limit": {"type": "integer", "description": "Maximum number of lines to read. Default: 5000, Max: 10000"}
			},
			"required": ["file_path"]
		}`),
	}
}

func writeFileTool() ToolDefinition {
	return ToolDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates the file and any parent directories if they don't exist. Overwrites existing files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "The path to the file to write. Relative paths are resolved against the working directory."},
				"content": {"type": "string", "description": "The content to write to the file."}
			},
			"required": ["file_path", "content"]
		}`),
	}
}

func editFileTool() ToolDefinition {
	return ToolDefinition{
		Name:        "edit_file",
		Description: "Edit a file by replacing old_string with new_string. Tries exact match first, then whitespace-tolerant line matching (Codex-style) if exact match fails. Use this for surgical edits rather than rewriting entire files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "The path to the file to edit. Relative paths are resolved against the working directory."},
				"old_string": {"type": "string", "description": "The exact string to find and replace. Must be unique within the file."},
				"new_string": {"type": "string", "description": "The replacement string."},
				"replace_all": {"type": "boolean", "description": "If true, replace all occurrences. Default: false"}
			},
			"required": ["file_path", "old_string", "new_string"]
		}`),
	}
}

func bashTool() ToolDefinition {
	return ToolDefinition{
		Name:        "bash",
		Description: "Execute a bash command and return its stdout and stderr. The command runs in the working directory. Use this for running tests, builds, git commands, and other shell operations. Commands default to a 600-second timeout when no positive timeout is provided.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "The bash command to execute."},
				"timeout": {"type": "integer", "description": "Timeout in seconds. Default: 600. Positive explicit values are allowed without a minimum or maximum cap."}
			},
			"required": ["command"]
		}`),
	}
}

func listFilesTool() ToolDefinition {
	return ToolDefinition{
		Name:        "list_files",
		Description: "List files and directories at a given path. Returns file names with '/' suffix for directories. Useful for exploring project structure.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Directory path to list. Relative paths are resolved against the working directory. Default: current working directory."},
				"recursive": {"type": "boolean", "description": "If true, list files recursively (max depth 4, max 500 entries). Default: false"},
				"pattern": {"type": "string", "description": "Glob pattern to filter results (e.g. '*.go', '*.ts'). Only applies to filenames, not paths."}
			}
		}`),
	}
}

func grepSearchTool() ToolDefinition {
	return ToolDefinition{
		Name:        "grep_search",
		Description: "Search file contents using a regular expression pattern. Returns matching lines with file paths and line numbers. Useful for finding function definitions, imports, references, etc.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Regular expression pattern to search for."},
				"path": {"type": "string", "description": "Directory or file to search in. Default: working directory."},
				"include": {"type": "string", "description": "Glob pattern to filter files (e.g. '*.go', '*.ts'). Default: all files."}
			},
			"required": ["pattern"]
		}`),
	}
}

const defaultExecBashTimeoutSeconds = 600

// ExecuteTool runs a tool locally and returns the output string.
// It resolves relative paths against workDir.
func ExecuteTool(ctx context.Context, workDir, name string, input json.RawMessage) (string, error) {
	return agenttools.Execute(ctx, workDir, name, input, agenttools.BashPolicy{
		DefaultTimeoutSeconds: defaultExecBashTimeoutSeconds,
	})
}
