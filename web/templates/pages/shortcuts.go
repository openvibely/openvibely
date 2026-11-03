package pages

// Shortcut descriptions mirror the handlers; contextual actions state their focus requirements.
type shortcutEntry struct {
	Action  string
	Keys    []string
	Context string
	MacOnly bool
}
type shortcutGroup struct {
	Name        string
	Description string
	Entries     []shortcutEntry
}

var shortcutGroups = []shortcutGroup{
	{Name: "Navigation", Description: "Move between projects, tasks, and automations.", Entries: []shortcutEntry{
		{Action: "Open project menu", Keys: []string{"mod", "shift", "K"}, Context: "Requires the project-menu button to be visible; other dialogs must be closed.", MacOnly: false},
		{Action: "Previous project tab", Keys: []string{"mod", "shift", "←"}, Context: "Requires at least two visible project tabs. Works from the composer; not other text fields.", MacOnly: false},
		{Action: "Next project tab", Keys: []string{"mod", "shift", "→"}, Context: "Requires at least two visible project tabs. Works from the composer; not other text fields.", MacOnly: false},
		{Action: "Open breadcrumb menu", Keys: []string{"mod", "K"}, Context: "On a task or automation page with a visible breadcrumb. Not on Settings or list pages.", MacOnly: false},
		{Action: "Previous task", Keys: []string{"mod", "shift", ","}, Context: "On task pages, including the composer. Stops at the start; New task opens the last item.", MacOnly: false},
		{Action: "Next task", Keys: []string{"mod", "shift", "."}, Context: "On task pages, including the composer. Stops at the end; New task opens the first item.", MacOnly: false},
		{Action: "Last visited task", Keys: []string{"mod", "shift", "L"}, Context: "Requires a previous task visit in this project and browser session. Works from the task composer.", MacOnly: false},
	}},
	{Name: "Task workspace", Description: "Navigate the task list and inspect your work.", Entries: []shortcutEntry{
		{Action: "Toggle task list", Keys: []string{"mod", "B"}, Context: "Show or hide the left task list.", MacOnly: false},
		{Action: "Previous task in list", Keys: []string{"mod", "shift", "↑"}, Context: "Task-list mode only; skips collapsed groups. Works from the composer, not other text fields.", MacOnly: false},
		{Action: "Next task in list", Keys: []string{"mod", "shift", "↓"}, Context: "Task-list mode only; skips collapsed groups. Works from the composer, not other text fields.", MacOnly: false},
		{Action: "Toggle task details", Keys: []string{"mod", "shift", "B"}, Context: "On task pages, including New task. Opens or closes the details panel.", MacOnly: false},
		{Action: "Toggle changes view", Keys: []string{"mod", "shift", "D"}, Context: "On a saved task. Not available on New task or while a modal details panel is open.", MacOnly: false},
		{Action: "Open task actions", Keys: []string{"shift", "F10"}, Context: "When a task row has focus.", MacOnly: false},
	}},
	{Name: "Composer", Description: "Write messages and revisit earlier inputs.", Entries: []shortcutEntry{
		{Action: "Send message", Keys: []string{"Enter"}, Context: "From the composer.", MacOnly: false},
		{Action: "Insert a new line", Keys: []string{"shift", "Enter"}, Context: "From the composer.", MacOnly: false},
		{Action: "Steer the current turn", Keys: []string{"mod", "Enter"}, Context: "When a response is running and steering is available.", MacOnly: false},
		{Action: "Previous message in history", Keys: []string{"↑"}, Context: "Composer must have saved message history. Start from the first line with no selection.", MacOnly: false},
		{Action: "Next message in history", Keys: []string{"↓"}, Context: "Only while browsing message history, with no selection; eventually restores your draft.", MacOnly: false},
		{Action: "Exit message history", Keys: []string{"Esc"}, Context: "Only while browsing composer message history. Restores your draft.", MacOnly: false},
		{Action: "Jump to previous message", Keys: []string{"⌘", "↑"}, Context: "In the conversation or its composer. macOS only.", MacOnly: true},
		{Action: "Jump to next message", Keys: []string{"⌘", "↓"}, Context: "In the conversation or its composer. macOS only.", MacOnly: true},
	}},
	{Name: "Menus & panels", Description: "Use these when the relevant control has keyboard focus.", Entries: []shortcutEntry{
		{Action: "Move through picker results", Keys: []string{"↑", "or", "↓"}, Context: "With the project or breadcrumb picker open and focused.", MacOnly: false},
		{Action: "Choose a picker result", Keys: []string{"Enter"}, Context: "With the project or breadcrumb picker open and focused.", MacOnly: false},
		{Action: "Close the picker", Keys: []string{"Esc"}, Context: "With the project or breadcrumb picker open.", MacOnly: false},
		{Action: "Focus adjacent project tab", Keys: []string{"←", "or", "→"}, Context: "Focus a project tab first. Moves focus; Enter opens that project.", MacOnly: false},
		{Action: "Focus first or last project tab", Keys: []string{"Home", "or", "End"}, Context: "Focus a project tab first. Moves focus; Enter opens that project.", MacOnly: false},
		{Action: "Close a project tab", Keys: []string{"forwardDelete"}, Context: "Focus the project tab first. Uses forward delete, not the Mac backspace key.", MacOnly: false},
	}},
	{Name: "Automations", Description: "Navigation on automation pages; editor shortcuts only in the builder.", Entries: []shortcutEntry{
		{Action: "Previous automation", Keys: []string{"mod", "shift", ","}, Context: "On an existing automation. Does not run inside text fields or the YAML editor.", MacOnly: false},
		{Action: "Next automation", Keys: []string{"mod", "shift", "."}, Context: "On an existing automation. Does not run inside text fields or the YAML editor.", MacOnly: false},
		{Action: "Last visited automation", Keys: []string{"mod", "shift", "L"}, Context: "Requires a previous automation visit. Does not run inside text fields or the YAML editor.", MacOnly: false},
		{Action: "Indent YAML", Keys: []string{"Tab"}, Context: "Insert two spaces in the YAML editor.", MacOnly: false},
		{Action: "Unindent YAML", Keys: []string{"shift", "Tab"}, Context: "Remove up to two leading spaces.", MacOnly: false},
		{Action: "Remove a connection", Keys: []string{"Delete", "or", "Backspace"}, Context: "When a connection has focus.", MacOnly: false},
	}},
	{Name: "File previews", Description: "Use these while a file preview is open.", Entries: []shortcutEntry{
		{Action: "Previous file", Keys: []string{"←"}, Context: "Outside text fields and spreadsheet grids.", MacOnly: false},
		{Action: "Next file", Keys: []string{"→"}, Context: "Outside text fields and spreadsheet grids.", MacOnly: false},
		{Action: "Zoom in", Keys: []string{"+"}, Context: "In image and PDF previews.", MacOnly: false},
		{Action: "Zoom out", Keys: []string{"−"}, Context: "In image and PDF previews.", MacOnly: false},
		{Action: "Reset image zoom", Keys: []string{"0"}, Context: "In image previews.", MacOnly: false},
		{Action: "Previous PDF page", Keys: []string{"PageUp", "or", "↑"}, Context: "In a PDF preview.", MacOnly: false},
		{Action: "Next PDF page", Keys: []string{"PageDown", "or", "↓"}, Context: "In a PDF preview.", MacOnly: false},
		{Action: "Close preview", Keys: []string{"Esc"}, Context: "Return to the conversation.", MacOnly: false},
	}},
	{Name: "Schedule", Description: "Shortcuts for the schedule calendar.", Entries: []shortcutEntry{
		{Action: "Select a date or time", Keys: []string{"Enter", "or", "Space"}, Context: "When a calendar day or hour has focus.", MacOnly: false},
		{Action: "Clear selection", Keys: []string{"Esc"}, Context: "Also closes menus and cancels an active gesture.", MacOnly: false},
	}},
}
