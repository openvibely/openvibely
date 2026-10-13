package agenttools

import (
	"fmt"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

type Definition struct {
	Name        string
	Description string
	Schema      *toolschema.Schema
}

func Definitions(policy BashPolicy) []Definition {
	return []Definition{readFileTool(), writeFileTool(), editFileTool(), bashTool(policy), listFilesTool(), grepSearchTool()}
}
func readFileTool() Definition {
	return Definition{
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
}

func writeFileTool() Definition {
	return Definition{
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
}

func editFileTool() Definition {
	return Definition{
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
}

func bashTool(policy BashPolicy) Definition {
	description := fmt.Sprintf("Execute a bash command and return its stdout and stderr. The command runs in the working directory. Use this for running tests, builds, git commands, and other shell operations. Commands have a default timeout of %d seconds.", policy.DefaultTimeoutSeconds)
	timeoutDescription := fmt.Sprintf("Timeout in seconds. Default: %d.", policy.DefaultTimeoutSeconds)
	if policy.MaxTimeoutSeconds > 0 {
		timeoutDescription += fmt.Sprintf(" Max: %d.", policy.MaxTimeoutSeconds)
	} else {
		timeoutDescription += " Positive explicit values are allowed without a minimum or maximum cap."
	}

	return Definition{
		Name:        "bash",
		Description: description,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"command": {
					Type:        "string",
					Description: "The bash command to execute.",
				},
				"timeout": {
					Type:        "integer",
					Description: timeoutDescription,
				},
			},
			Required:             []string{"command"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	}
}

func listFilesTool() Definition {
	return Definition{
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
}

func grepSearchTool() Definition {
	return Definition{
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
}
