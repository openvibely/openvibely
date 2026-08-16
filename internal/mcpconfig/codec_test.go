package mcpconfig

import (
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBuildMCPJSONRoundTripsRuntimeFields(t *testing.T) {
	data, err := BuildMCPJSON([]models.MCPServerConfig{{
		Name:    "runtime-http",
		Type:    "http",
		Command: []string{"node", "server.js"},
		URL:     "https://example.test/mcp",
		Env:     map[string]string{"TOKEN": "secret"},
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}})
	if err != nil {
		t.Fatalf("BuildMCPJSON: %v", err)
	}

	servers, err := ParseNestedServers(data, ParseOptions{})
	if err != nil {
		t.Fatalf("ParseNestedServers: %v", err)
	}
	want := []models.MCPServerConfig{{
		Name:    "runtime-http",
		Type:    "http",
		Command: []string{"node", "server.js"},
		URL:     "https://example.test/mcp",
		Env:     map[string]string{"TOKEN": "secret"},
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("round-tripped servers = %#v, want %#v", servers, want)
	}
}
