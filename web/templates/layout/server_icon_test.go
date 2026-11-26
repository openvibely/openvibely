package layout

import (
	"context"
	"strings"
	"testing"
)

func TestServerIconCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ServerIconChoices() {
		if seen[c.Name] {
			t.Fatalf("duplicate %s", c.Name)
		}
		seen[c.Name] = true
		var out strings.Builder
		if err := ServerIcon(c.Name, "server").Render(context.Background(), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "<svg") {
			t.Fatal(c.Name)
		}
		if c.Name == "cherry" && (!strings.Contains(c.Categories, "Food & drink") || !strings.Contains(c.Categories, "Nature & weather")) {
			t.Fatal(c)
		}
	}
	if len(seen) != 324 {
		t.Fatal(len(seen))
	}
	if ValidServerIcon("../server") {
		t.Fatal("invalid accepted")
	}
	var fallback, automatic strings.Builder
	ServerIcon("unknown", "laptop").Render(context.Background(), &fallback)
	MachineIcon("laptop").Render(context.Background(), &automatic)
	if fallback.String() != automatic.String() {
		t.Fatal("fallback mismatch")
	}
}
