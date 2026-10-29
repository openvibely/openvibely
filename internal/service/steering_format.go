package service

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// FormatQueuedSteeringText trims queued steering notes, drops blank notes, and
// joins the remaining text with a blank line.
func FormatQueuedSteeringText(inputs []models.ThreadInput) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if content := strings.TrimSpace(input.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
