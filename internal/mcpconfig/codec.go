package mcpconfig

import (
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// ParseOptions controls caller-specific MCP config policy around the shared JSON shape.
type ParseOptions struct {
	// AllowDirectServers accepts a top-level map of server names to definitions when
	// the file does not contain a valid nested mcpServers object.
	AllowDirectServers bool
	// InferType fills empty Type as http when URL is set, otherwise stdio.
	InferType bool
	// MapValueTransform is applied to parsed env/header values after trimming.
	MapValueTransform func(string) string
}

// ParseNestedServers parses {"mcpServers": {name: server}} JSON.
func ParseNestedServers(data []byte, opts ParseOptions) ([]models.MCPServerConfig, error) {
	var root struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return parseServerMap(root.MCPServers, opts), nil
}

// ParseServers parses MCP server JSON, accepting either nested mcpServers or,
// when enabled by the caller, a direct top-level server map.
func ParseServers(data []byte, opts ParseOptions) ([]models.MCPServerConfig, error) {
	if !opts.AllowDirectServers {
		return ParseNestedServers(data, opts)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	serverRaw := root
	if mcpServersRaw, ok := root["mcpServers"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(mcpServersRaw, &nested); err == nil {
			serverRaw = nested
		}
	}
	return parseServerMap(serverRaw, opts), nil
}

func parseServerMap(serverRaw map[string]json.RawMessage, opts ParseOptions) []models.MCPServerConfig {
	servers := make([]models.MCPServerConfig, 0, len(serverRaw))
	for name, raw := range serverRaw {
		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}
		server := models.MCPServerConfig{
			Name:    strings.TrimSpace(name),
			Type:    strings.TrimSpace(asString(payload["type"])),
			Command: commandParts(payload["command"]),
			URL:     strings.TrimSpace(asString(payload["url"])),
			Env:     stringMap(payload["env"], opts.MapValueTransform),
			Headers: stringMap(payload["headers"], opts.MapValueTransform),
		}
		server.Command = append(server.Command, commandParts(payload["args"])...)
		if opts.InferType && server.Type == "" {
			if server.URL != "" {
				server.Type = "http"
			} else {
				server.Type = "stdio"
			}
		}
		servers = append(servers, server)
	}
	return servers
}

// BuildMCPJSON serializes MCP servers in the nested .mcp.json shape.
func BuildMCPJSON(servers []models.MCPServerConfig) ([]byte, error) {
	type serverJSON struct {
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command,omitempty"`
		Args    []string          `json:"args,omitempty"`
		URL     string            `json:"url,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	root := struct {
		MCPServers map[string]serverJSON `json:"mcpServers"`
	}{MCPServers: map[string]serverJSON{}}
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		entry := serverJSON{
			Type:    strings.TrimSpace(server.Type),
			URL:     strings.TrimSpace(server.URL),
			Env:     cleanMap(server.Env),
			Headers: cleanMap(server.Headers),
		}
		command := cleanSlice(server.Command)
		if len(command) > 0 {
			entry.Command = command[0]
			if len(command) > 1 {
				entry.Args = command[1:]
			}
		}
		root.MCPServers[name] = entry
	}
	return json.MarshalIndent(root, "", "  ")
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringMap(v interface{}, transform func(string) string) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]interface{})
	if !ok {
		return out
	}
	for k, vv := range m {
		if s, ok := vv.(string); ok {
			trimmedKey := strings.TrimSpace(k)
			if trimmedKey == "" {
				continue
			}
			value := strings.TrimSpace(s)
			if transform != nil {
				value = transform(value)
			}
			out[trimmedKey] = value
		}
	}
	return out
}

func commandParts(v interface{}) []string {
	var parts []string
	switch vv := v.(type) {
	case string:
		vv = strings.TrimSpace(vv)
		if vv != "" {
			parts = append(parts, vv)
		}
	case []interface{}:
		for _, item := range vv {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return parts
}

func cleanSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cleanMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range values {
		k = strings.TrimSpace(k)
		if k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}
