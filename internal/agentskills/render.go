package agentskills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RenderAvailableSkillsMarkdown returns the literal contents of top-level
// skills/SKILLS.md index files (global + project), wrapped with a short
// instruction header and fenced inside <available_skills>...</available_skills>.
// The route hook reads this block to choose relevant standalone skills; normal
// task turns receive only the selected subset rendered by
// RenderSelectedSkillsMarkdown.
func RenderAvailableSkillsMarkdown(globalRoot, projectRoot string) string {
	var sb strings.Builder
	sb.WriteString("## Available Standalone Skills\n\n")
	sb.WriteString("Review the standalone skill index below and select skill handles relevant to the user prompt.\n")
	sb.WriteString("Agents are assigned outside this routing step; do not choose or switch agents. When a\n")
	sb.WriteString("listed standalone skill is relevant, return its skill handle, for example `debug_go_tests`.\n")
	sb.WriteString("Use `skills_list` or `skill_view` only to inspect available skills. Use `agent_view` only to understand an assigned agent.\n\n")
	sb.WriteString("<available_skills>\n")

	wrote := false
	if section := readStandaloneSkillsSection(globalRoot, "global"); section != "" {
		sb.WriteString(section)
		wrote = true
	}
	if section := readStandaloneSkillsSection(projectRoot, "project"); section != "" {
		if wrote {
			sb.WriteString("\n")
		}
		sb.WriteString(section)
		wrote = true
	}
	if !wrote {
		sb.WriteString("_No standalone skills indexed in this turn._\n")
	}
	sb.WriteString("</available_skills>\n")
	return sb.String()
}

// RenderAvailableAgentSkillsMarkdown returns the assigned agent's SKILLS.md
// index from global/project roots for router selection.
func RenderAvailableAgentSkillsMarkdown(globalRoot, projectRoot, agentKey string) string {
	var sb strings.Builder
	sb.WriteString("## Available Assigned-Agent Skills\n\n")
	fmt.Fprintf(&sb, "Review the skills owned by assigned agent `%s` below and select skill handles relevant to the user prompt.\n", strings.TrimSpace(agentKey))
	sb.WriteString("Do not choose or switch agents. When a listed assigned-agent skill is relevant, return only its skill key, for example `maintain_skill_library`, not `agent/skill`.\n")
	sb.WriteString("Use `skill_view` only to inspect skills listed for this assigned agent.\n\n")
	sb.WriteString("<available_skills>\n")

	wrote := false
	if section := readAgentSkillsSection(globalRoot, agentKey, "global"); section != "" {
		sb.WriteString(section)
		wrote = true
	}
	if section := readAgentSkillsSection(projectRoot, agentKey, "project"); section != "" {
		if wrote {
			sb.WriteString("\n")
		}
		sb.WriteString(section)
		wrote = true
	}
	if !wrote {
		fmt.Fprintf(&sb, "_No skills indexed for assigned agent `%s` in this turn._\n", strings.TrimSpace(agentKey))
	}
	sb.WriteString("</available_skills>\n")
	return sb.String()
}

// RenderSelectedSkillsMarkdown renders the exact catalog skill handles selected
// for the current task turn. The model may load these skills with skill_view;
// unrelated catalog handles are intentionally omitted from this prompt block.
func RenderSelectedSkillsMarkdown(catalog *Catalog, handles []string) string {
	if catalog == nil || len(handles) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Selected Skills For This Task\n\n")
	if catalog.IsAgentOwned() {
		sb.WriteString("The lifecycle router selected these assigned-agent skills for this turn. Load any needed full body with `skill_view(\"<skill>\")`.\n\n")
	} else {
		sb.WriteString("The task keeps its assigned/default agent. The lifecycle router selected these standalone skills for this turn. Load any needed full body with `skill_view(\"<skill>\")`.\n\n")
	}
	sb.WriteString("<selected_skills>\n")
	wrote := false
	seen := map[string]struct{}{}
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		entry, ok := catalog.Lookup(handle)
		if !ok {
			continue
		}
		seen[handle] = struct{}{}
		if entry.AgentKey != "" {
			fmt.Fprintf(&sb, "- `%s` (agent:%s)\n", entry.Handle, entry.AgentKey)
		} else {
			fmt.Fprintf(&sb, "- `%s` (%s)\n", entry.Handle, entry.Source)
		}
		wrote = true
	}
	if !wrote {
		return ""
	}
	sb.WriteString("</selected_skills>\n")
	return sb.String()
}

func readStandaloneSkillsSection(root, scope string) string {
	if root == "" {
		return ""
	}
	skillsDir := filepath.Join(root, SkillsDir)
	return readSkillsIndexSectionFiltered(SkillsIndexPath(root), skillsDir, "", scope)
}

func readAgentSkillsSection(root, agentKey, scope string) string {
	agentKey = strings.TrimSpace(agentKey)
	if root == "" || agentKey == "" {
		return ""
	}
	skillsDir := filepath.Join(root, AgentRootsDir, agentKey, SkillsDir)
	return readSkillsIndexSectionFiltered(AgentSkillsIndexPath(root, agentKey), skillsDir, agentKey, scope)
}

// filteredIndexBody reads a SKILLS.md index file and returns the content with
// disabled-skill sections removed. agentKey, when non-empty, constrains
// matching to sections whose h2 header starts with "<agentKey>/". Returns
// (filteredContent, true) when the file exists and has at least one enabled
// section; ("", false) otherwise. This is the shared core used by both
// readSkillsIndexSectionFiltered (available_skills context injection) and
// resolveSkillsList (skills_list tool output) so that disabled handles are
// absent from every surface the route_task model can inspect.
func filteredIndexBody(indexPath, skillsDir, agentKey string) (string, bool) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return "", false
	}
	content := string(data)

	type section struct {
		body  string // full text from this ## header to the next
		skill string // bare skill slug (agentKey prefix stripped)
	}

	headerLocs := h2HeaderRegexp.FindAllStringIndex(content, -1)
	headerMatches := h2HeaderRegexp.FindAllStringSubmatch(content, -1)

	var sections []section
	for i, loc := range headerLocs {
		handle := strings.TrimSpace(headerMatches[i][1])
		skill := handle
		if agentKey != "" {
			prefix := agentKey + "/"
			if !strings.HasPrefix(handle, prefix) {
				continue
			}
			skill = strings.TrimPrefix(handle, prefix)
		}
		if strings.Contains(skill, "/") || !isValidSlug(skill) {
			continue
		}
		end := len(content)
		if i+1 < len(headerLocs) {
			end = headerLocs[i+1][0]
		}
		sections = append(sections, section{body: content[loc[0]:end], skill: skill})
	}

	var sb strings.Builder
	for _, sec := range sections {
		absPath := filepath.Join(skillsDir, sec.skill, SkillFile)
		if _, err := os.Stat(absPath); err != nil {
			continue
		}
		if skillDisabledOnDisk(absPath) {
			continue
		}
		sb.WriteString(sec.body)
	}
	out := sb.String()
	if out == "" {
		return "", false
	}
	return out, true
}

// readSkillsIndexSectionFiltered reads a SKILLS.md index file and returns only
// the sections for skills that are not disabled on disk, wrapped with a scope
// header. agentKey, when set, constrains matching to the agent's skill handles.
// This prevents disabled skill handles from appearing in the route_task
// available_skills context injection.
func readSkillsIndexSectionFiltered(indexPath, skillsDir, agentKey, scope string) string {
	body, ok := filteredIndexBody(indexPath, skillsDir, agentKey)
	if !ok {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "### Scope: %s\n\n", scope)
	out.WriteString(strings.TrimSpace(body))
	out.WriteString("\n")
	return out.String()
}
