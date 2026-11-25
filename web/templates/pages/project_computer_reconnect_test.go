package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// Every computer script sends an expired remote sign-in to the sign-in screen.
func TestProjectComputerScriptsOpenSignInWhenRemoteSessionExpires(t *testing.T) {
	var page bytes.Buffer
	for _, c := range []templ.Component{
		ProjectComputerContent("r-0123456789abcdef-p1", "r-0123456789abcdef-t1"),
		ProjectComputerToggle("r-0123456789abcdef-p1"),
		ProjectComputerPermissions("r-0123456789abcdef-p1", ""),
		TaskComputerToggle("r-0123456789abcdef-p1", "r-0123456789abcdef-t1"),
	} {
		if err := c.Render(context.Background(), &page); err != nil {
			t.Fatal(err)
		}
	}
	html := page.String()
	if got := strings.Count(html, "openVibelyReconnect?.fetch(res)"); got != 4 {
		t.Fatalf("computer scripts routing fetches through reconnect = %d, want 4", got)
	}
	if !strings.Contains(html, "openVibelyReconnect?.frame(event.data)") {
		t.Fatal("live computer status does not open sign-in")
	}
}
