# Keyboard shortcuts

These are the app's keyboard bindings. On macOS, **Command** is ⌘ and **Option** is ⌥. On Windows and Linux, use **Ctrl** and **Alt** as shown below. A `+` means hold the keys together.

## Navigation

| Action | macOS | Windows / Linux |
| --- | --- | --- |
| Toggle left navigation | Command+B | Ctrl+B |
| Toggle task details panel | Command+Shift+B | Ctrl+Shift+B |
| Toggle task thread / Changes | Command+Shift+D | Ctrl+Shift+D |
| Open or close the task / automation breadcrumb selector | Command+K | Ctrl+K |
| Open or close the project menu | Command+Shift+K | Ctrl+Shift+K |
| Previous project tab | Command+Shift+← | Ctrl+Shift+← |
| Next project tab | Command+Shift+→ | Ctrl+Shift+→ |
| Previous task in the breadcrumb list | Option+↑ | Alt+↑ |
| Next task in the breadcrumb list | Option+↓ | Alt+↓ |
| Switch to the last task visited | Option+L | Alt+L |

Project-tab shortcuts follow the displayed tab order and wrap at either end. They require at least two tabs and a visible tab strip. They work while the chat or task composer is focused. They are ignored inside other text fields, editors, and select controls, and while a dialog or popover is open.

Command/Ctrl+K also opens the breadcrumb selector on automation view/edit pages. Command/Ctrl+Shift+K toggles the project menu wherever its button is visible.

The task-specific shortcuts are available on a task page. All of them work while the task composer is focused. Option/Alt task-switching shortcuts are ignored in other text fields and editors.

- **Previous/next task** uses the same project-scoped list and ordering as the breadcrumb selector, without opening it. This is not a history of pages you visited: the list includes task grouping and update recency. The order stays fixed while you step through it, and navigation stops at either end. Opening a different task another way resets the list for the next cycle.
- **Last task visited** switches between the last two distinct tasks visited within the current project and browser tab. It does nothing until a previous task is available.
- Task switching preserves the current composer draft. Toggling the task details panel keeps composer focus when the panel is docked; an overlay panel takes focus while open.
- Thread / Changes toggling works on saved tasks, including while the composer is focused, and preserves the draft and thread scroll intent. It pauses while an overlay details panel or popover is open. Chrome also assigns this combination to bookmarking all tabs.
- Task shortcuts pause while a dialog is open, except that Command/Ctrl+K can close the task breadcrumb selector itself.

Browser or desktop-webview back/forward navigation is separate from these task-list shortcuts. Browser and operating-system bindings may intercept a key combination before the app receives it.

## Composer

These controls apply to chat and task-thread composers.

| Action | macOS | Windows / Linux |
| --- | --- | --- |
| Send or queue a message | Enter | Enter |
| Insert a newline | Shift+Enter | Shift+Enter |
| Steer an active turn | Command+Enter | Ctrl+Enter |
| Recall an older sent message | ↑ | ↑ |
| Recall a newer message or return to the draft | ↓ | ↓ |
| Exit message-history browsing and restore the draft | Escape | Escape |

Command/Ctrl+Enter requests steering when supported and an active turn is available; otherwise it follows normal submission behavior. Holding Command/Ctrl while clicking the primary composer action also requests steering.

Message-history navigation uses **unmodified** Up/Down arrows. It is available when the composer is empty or the caret is at the very start of the text with no selection. Down only browses history after Up has entered it. Escape restores the draft saved when history browsing began. Chat and individual task threads have separate message histories.

Option/Alt+Up/Down in the task composer switches tasks rather than browsing sent messages.

## Selectors and panels

| Context | Keys | Action |
| --- | --- | --- |
| Focused searchable-selector button | Enter, Space, or ↓ | Open the selector and focus search |
| Open searchable selector | ↑ / ↓ | Move between options |
| Open searchable selector | Enter | Choose the highlighted option |
| Focused selector option | Space | Choose the option |
| Open searchable selector | Escape | Close and return focus to its button |
| Task details tabs | ← / → | Move between tabs |
| Task details tabs | Home / End | Select the first / last tab |
| Focused task-panel divider | ← / → | Resize the panel |
| Task details overlay | Escape | Close the panel |

Selector arrow navigation ignores Option/Alt, Command, and Ctrl modifiers, so it does not consume task-switching shortcuts after the selector closes.

## Image viewer

These shortcuts apply while the image viewer is open.

| Keys | Action |
| --- | --- |
| ← / → | Previous / next image |
| `+` or `=` | Zoom in |
| `-` | Zoom out |
| 0 | Reset zoom |
| Escape | Close the viewer |

On a US keyboard, `+` and `=` share a key. Both are accepted so you can zoom in with or without Shift. Zoom out uses the unshifted `-` key; Shift+`-` produces `_`, which is not currently a zoom shortcut.

## Other contextual controls

| Context | Keys | Action |
| --- | --- | --- |
| Project tabs | ← / →, Home / End | Focus the previous/next or first/last project tab |
| Project tab | Enter / Space | Activate the focused project |
| Project tab | Delete | Close the project tab |
| Project tab | Shift+F10 / Context Menu key | Open the tab menu |
| Task-panel divider | Home / End | Set minimum / maximum panel width |
| Automation graph node | Arrow keys | Move the node by 10 units |
| Automation graph node or edge | Delete / Backspace | Remove the focused node or edge |
| Automation connection handle | Enter / Space | Select an output, then connect to an input |
| Automation YAML editor | Tab / Shift+Tab | Indent / unindent |
| Diff comment editor | Enter / Shift+Enter | Submit comment / insert newline |
| Diff comment editor | Escape | Cancel editing |

Task-detail and project-tab navigation uses unmodified keys. Image-viewer controls ignore Command, Ctrl, and Option/Alt; Shift remains supported for `+`. Property-picker selection waits until text composition has finished. Escape in a foreground dialog or menu does not clear the card selection underneath it.
