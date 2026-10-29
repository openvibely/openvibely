package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestFormatQueuedSteeringText(t *testing.T) {
	tests := []struct {
		name   string
		inputs []models.ThreadInput
		want   string
	}{
		{
			name:   "multiple notes",
			inputs: []models.ThreadInput{{Content: "note one"}, {Content: "note two"}},
			want:   "note one\n\nnote two",
		},
		{
			name: "trims notes and skips blanks",
			inputs: []models.ThreadInput{
				{Content: "  note one \n"},
				{Content: " \t\n "},
				{Content: "\tnote two  "},
			},
			want: "note one\n\nnote two",
		},
		{
			name:   "single note",
			inputs: []models.ThreadInput{{Content: "  one note  "}},
			want:   "one note",
		},
		{
			name:   "empty input",
			inputs: nil,
			want:   "",
		},
		{
			name:   "whitespace-only notes",
			inputs: []models.ThreadInput{{Content: " \t\n "}, {Content: "\n"}},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatQueuedSteeringText(tt.inputs); got != tt.want {
				t.Errorf("FormatQueuedSteeringText() = %q, want %q", got, tt.want)
			}
		})
	}
}
