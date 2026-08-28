package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestFallbackProjectID(t *testing.T) {
	tests := []struct {
		name     string
		projects []models.Project
		want     string
	}{
		{
			name: "empty list",
			want: "",
		},
		{
			name: "default project",
			projects: []models.Project{
				{ID: "default", IsDefault: true},
				{ID: "first"},
			},
			want: "default",
		},
		{
			name: "default project is not first",
			projects: []models.Project{
				{ID: "first"},
				{ID: "default", IsDefault: true},
			},
			want: "default",
		},
		{
			name: "no default project",
			projects: []models.Project{
				{ID: "first"},
				{ID: "second"},
			},
			want: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fallbackProjectID(tt.projects); got != tt.want {
				t.Fatalf("fallbackProjectID() = %q, want %q", got, tt.want)
			}
		})
	}
}
