package pages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestProjectSharingTransferEligibility(t *testing.T) {
	owner := models.OrgUser{Principal: models.Principal{UserID: "owner", Username: "Owner"}}
	disabled := models.OrgUser{Principal: models.Principal{UserID: "disabled", Username: "Disabled"}, Disabled: true}
	active := models.OrgUser{Principal: models.Principal{UserID: "active", Username: "Active"}}
	for _, tc := range []struct {
		name                 string
		users                []models.OrgUser
		canTransfer, enabled bool
	}{
		{"sole owner", []models.OrgUser{owner}, true, false},
		{"disabled recipient", []models.OrgUser{owner, disabled}, true, false},
		{"active recipient", []models.OrgUser{owner, disabled, active}, true, true},
		{"maintainer", []models.OrgUser{owner, active}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, ProjectSharing(&models.ProjectAccess{ProjectID: "project", OwnerUserID: "owner", OrgRole: "editor"}, nil, tc.users, nil, tc.canTransfer, "").Render(t.Context(), &out))
			root, err := html.Parse(strings.NewReader(out.String()))
			require.NoError(t, err)
			found := false
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "button" && n.FirstChild != nil && (n.FirstChild.Data == "Transfer ownership…" || n.FirstChild.Data == "Review transfer") {
					isDisabled := false
					for _, a := range n.Attr {
						if a.Key == "disabled" {
							isDisabled = true
						}
					}
					require.Equal(t, !tc.enabled, isDisabled)
				}
				if n.Type == html.ElementNode && n.Data == "select" {
					label, isDisabled := "", false
					for _, a := range n.Attr {
						if a.Key == "aria-label" {
							label = a.Val
						}
						if a.Key == "disabled" {
							isDisabled = true
						}
					}
					if label == "New owner" {
						found = true
						require.Equal(t, !tc.enabled, isDisabled)
						for child := n.FirstChild; child != nil; child = child.NextSibling {
							for _, a := range child.Attr {
								if a.Key == "value" {
									require.Equal(t, "active", a.Val, "only an active non-owner may be offered")
								}
							}
						}
					}
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}
			walk(root)
			require.Equal(t, tc.canTransfer, found)
			require.Equal(t, tc.canTransfer && !tc.enabled, strings.Contains(out.String(), "Add another active organization member"))
			require.NotContains(t, out.String(), `value="private"`)
		})
	}
}
