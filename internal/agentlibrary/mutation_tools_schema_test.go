package agentlibrary

import (
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/toolschema"
	"strings"
	"testing"
)

func TestMutationToolSchemasAreValidJSONAndIncludeRequiredHints(t *testing.T) {
	tools := MutationTools(&Importer{}, nil)
	if tools == nil {
		t.Fatal("MutationTools returned nil")
	}
	if len(tools.Definitions) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(tools.Definitions))
	}
	for _, def := range tools.Definitions {
		var v map[string]any
		if err := json.Unmarshal(def.ParameterSchema(), &v); err != nil {
			t.Fatalf("invalid JSON schema for %s: %v\nschema:\n%s", def.Name, err, string(def.ParameterSchema()))
		}
		props, ok := v["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing properties", def.Name)
		}
		switch def.Name {
		case "skill_manage":
			decl, ok := props["declaration"].(map[string]any)
			if !ok {
				t.Fatalf("%s: missing declaration property", def.Name)
			}
			descRaw, _ := decl["description"].(string)
			for _, want := range []string{"kind", "openvibely.agent_skill", "version", "skill.key", "Example"} {
				if !strings.Contains(descRaw, want) {
					t.Errorf("%s declaration.description missing %q\ngot: %s", def.Name, want, descRaw)
				}
			}
		case "skill_import":
			for _, prop := range []string{"source_path", "content", "package_name", "scope", "files"} {
				if _, ok := props[prop]; !ok {
					t.Errorf("%s schema missing %s property", def.Name, prop)
				}
			}
			for _, want := range []string{"normalizes", "YAML frontmatter", "SKILLS.md"} {
				if !strings.Contains(def.Description, want) {
					t.Errorf("%s description missing %q\ngot: %s", def.Name, want, def.Description)
				}
			}
		default:
			t.Fatalf("unexpected tool definition %q", def.Name)
		}
	}
}

func TestAgentSkillRequiredFieldsByRuntime(t *testing.T) {
	library := LibraryAgentSkillMutationTools(&Importer{}, nil).Definitions[0]
	assigned := AgentSkillMutationTools(&Importer{}, nil, "assigned", "project").Definitions[0]
	for _, input := range []string{`{"action":"create"}`, `{"action":"create","agent":null}`} {
		if _, err := toolschema.ValidateArguments(library.Schema, nil, json.RawMessage(input)); err == nil {
			t.Fatal("library accepted missing agent")
		}
		if _, err := toolschema.ValidateArguments(assigned.Schema, nil, json.RawMessage(input)); err != nil {
			t.Fatalf("assigned agent required model input: %v", err)
		}
	}
	if _, err := toolschema.ValidateArguments(library.Schema, nil, json.RawMessage(`{"action":"create","agent":"target"}`)); err != nil {
		t.Fatal(err)
	}
}
