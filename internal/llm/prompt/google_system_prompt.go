package prompt

import (
	"bytes"
	"embed"
	"errors"
	"strings"
	"text/template"

	"github.com/openvibely/openvibely/internal/models"
)

//go:embed google/*.tmpl
var googlePrompts embed.FS

type googlePromptData struct {
	IsAutonomous  bool
	CascadeConfig map[string]any
	flags         map[string]bool
}

func (d googlePromptData) ExperimentEnabled(name string) bool { return d.flags[name] }

type limitedPromptBuffer struct{ bytes.Buffer }

func (b *limitedPromptBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 128<<10 {
		return 0, errors.New("prompt section too large")
	}
	return b.Buffer.Write(p)
}

// GoogleBasePrompt selects bundled model guidance. It performs no configuration
// requests; snapshots change only when the application is updated.
func GoogleBasePrompt(model string, autonomous bool, tools []string) string {
	family := model
	for _, suffix := range []string{"-tiered-high", "-tiered-xhigh", "-low", "-medium", "-high", "-xhigh", "-tiered"} {
		if strings.HasSuffix(family, suffix) {
			family = strings.TrimSuffix(family, suffix)
			break
		}
	}
	overrides := map[string]string{}
	if family == "gemini-4-hydrogen" {
		raw, _ := googlePrompts.ReadFile("google/hydrogen_guidelines.tmpl")
		overrides["guidelines"] = string(raw)
	}
	base := renderGoogleBasePrompt(autonomous, tools, overrides, map[string]bool{"generic-system-prompt": true})
	switch family {
	case "gemini-4-hydrogen", "gemini-4-argon", "gemini-4-carbon", "gemini-3.8-flash":
		raw, _ := googlePrompts.ReadFile("google/background_guidance.tmpl")
		base += "\n\n" + string(raw)
	}
	if family == "gemini-4-argon" || family == "gemini-4-carbon" {
		base += "\nWhen updating the user about background work, respond in the user's language (defaulting to English). Do not mix languages in that update."
	}
	return base
}

// Unknown template settings fall back to the bundled base section.
func renderGoogleBasePrompt(autonomous bool, tools []string, overrides map[string]string, flags map[string]bool) string {
	var definitions []map[string]string
	for _, name := range tools {
		definitions = append(definitions, map[string]string{"GetName": name})
	}
	data := googlePromptData{IsAutonomous: autonomous, flags: flags, CascadeConfig: map[string]any{
		"GetPlannerConfig": map[string]any{
			"GetSupportsLatexRendering": false,
			"GetDeclarativeMixinConfig": map[string]any{"GetPromptSections": []any{}, "GetTools": definitions},
			"GetToolConfig":             map[string]any{"GetRunCommand": map[string]any{"GetForbidSearchCommands": false, "GetSearchFallbackMode": ""}},
		},
	}}
	render := func(source string) (string, error) {
		t, err := template.New("google").Option("missingkey=error").Parse(source)
		if err != nil {
			return "", err
		}
		var b limitedPromptBuffer
		if err = t.Execute(&b, data); err != nil {
			return "", err
		}
		return b.String(), nil
	}
	var sections []string
	for _, name := range []string{"identity", "guidelines", "communication_style", "function_call"} {
		source, _ := googlePrompts.ReadFile("google/" + name + ".tmpl")
		text, err := render(overrides[name])
		if err != nil || strings.TrimSpace(text) == "" {
			text, _ = render(string(source))
		}
		sections = append(sections, adaptGooglePrompt(text))
	}
	sections = append(sections, `OpenVibely runtime rules take precedence over the preceding model guidance:
- Use only tools actually supplied with this request and their declared argument schemas.
- Respect all approval gates and the user's authorized scope. Never infer approval from silence.
- File links use [name](/absolute/path:line), not file:// URLs or #L fragments.
- Parallel calls are allowed only when supported by the supplied tools. Otherwise run independent calls sequentially.
- Background completion and waiting instructions apply only after a tool explicitly reports background work; do not assume background tools exist.`)
	return strings.Join(sections, "\n\n")
}
func adaptGooglePrompt(text string) string {
	return strings.NewReplacer("file://", "", "#L10-L20", ":10", "run_command", "shell", "view_file", "read_file", "StartLine and EndLine", "offset and limit").Replace(text)
}
func BuildGoogleAgentSystemPrompt(base, instructions string, workDir ...string) string {
	return buildAgentSystemPrompt(base, instructions, workDir...)
}
func BuildGoogleChatSystemPrompt(base string, followup bool, mode models.ChatMode, context string, restrict bool) string {
	text := buildChatSystemPrompt(base, followup, mode, context, restrict)
	if !followup {
		return base + "\n\nThe following OpenVibely chat-mode constraints take precedence:\n" + text
	}
	return text
}
