package agentlibrary

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationRejectsSymlinkDestinations(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, alias := range []string{"directory", "main", "index", "support", "ancestor"} {
			t.Run(alias+map[bool]string{false: "Standalone", true: "Agent"}[owned], func(t *testing.T) {
				root := t.TempDir()
				outside := t.TempDir()
				sentinel := filepath.Join(outside, "SKILL.md")
				require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0600))
				dir := filepath.Join(root, "skills", "sample")
				index := filepath.Join(root, "skills", "SKILLS.md")
				if owned {
					dir = filepath.Join(root, "agents", "owner", "skills", "sample")
					index = filepath.Join(root, "agents", "owner", "SKILLS.md")
				}
				require.NoError(t, os.MkdirAll(dir, 0755))
				switch alias {
				case "directory":
					require.NoError(t, os.Remove(dir))
					require.NoError(t, os.Symlink(outside, dir))
				case "main":
					require.NoError(t, os.Symlink(sentinel, filepath.Join(dir, "SKILL.md")))
				case "index":
					require.NoError(t, os.Symlink(sentinel, index))
				case "support":
					require.NoError(t, os.Symlink(outside, filepath.Join(dir, "references")))
				case "ancestor":
					parent := filepath.Dir(dir)
					require.NoError(t, os.RemoveAll(parent))
					require.NoError(t, os.Symlink(outside, parent))
				}
				importer := NewImporter(SkillRoots{Project: root}, nil)
				tool := "skill_manage"
				runtime := SkillMutationTools(importer, nil)
				args := map[string]any{"action": "create", "scope": "project", "declaration": "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: sample\n---\nChanged"}
				if owned {
					tool = "agent_skill_manage"
					runtime = LibraryAgentSkillMutationTools(importer, nil)
					args["agent"] = "owner"
				}
				if alias == "support" {
					args["action"] = "write_file"
					args["handle"] = "sample"
					args["support"] = map[string]string{"kind": "references", "path": "SKILL.md", "content": "Changed"}
				}
				input, err := json.Marshal(args)
				require.NoError(t, err)
				out, handled, failed, err := runtime.Executor(context.Background(), tool, input)
				require.NoError(t, err)
				require.True(t, handled)
				require.True(t, failed, out)
				data, err := os.ReadFile(sentinel)
				require.NoError(t, err)
				require.Equal(t, "untouched", string(data))
			})
		}
	}
}
