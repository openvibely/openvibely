package pages

import "testing"

func TestAutomationConnectionPath(t *testing.T) {
	for _, tt := range []struct {
		name           string
		x1, y1, x2, y2 float64
		want           string
	}{
		{"aligned", 180, 45, 250, 45, "M180 45 L250 45"},
		{"branch", 180, 45, 250, 175, "M180 45 L188 45 C223 45 207 175 242 175 L250 175"},
		{"merge", 180, 175, 250, 45, "M180 175 L188 175 C223 175 207 45 242 45 L250 45"},
		{"backward", 500, 45, 250, 175, "M500 45 L508 45 C635.68 45 114.32000000000001 175 242 175 L250 175"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := automationConnectionPath(tt.x1, tt.y1, tt.x2, tt.y2); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
