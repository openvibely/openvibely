package components

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"testing"
)

func TestComposerDestinationLabel(t *testing.T) {
	ctx := templateui.WithServerNames(context.Background(), map[string]string{"0123456789abcdef": "oldmac"})
	for _, tc := range []struct {
		id, name string
		shared   bool
		want     string
	}{
		{"local", "Repo · This computer", true, "This computer"},
		{remoteid.ID("0123456789abcdef", "p"), "Repo · oldmac", true, "oldmac"},
		{remoteid.ID("0123456789abcdef", "p"), "Repo · oldmac", false, "Repo · oldmac"},
		{"local", "Repo · This computer (~/second-copy)", true, "This computer (~/second-copy)"},
	} {
		if got := composerDestinationLabel(ctx, models.Project{ID: tc.id, Name: tc.name}, tc.shared); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
