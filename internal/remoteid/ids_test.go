package remoteid

import "testing"

const server = "0123456789abcdef"

func TestIDRoundTrip(t *testing.T) {
	id := ID(server, "abc")
	if id != "r-0123456789abcdef-abc" {
		t.Fatalf("ID = %q", id)
	}
	if ID(server, id) != id || ID(server, "") != "" {
		t.Fatal("ID must not re-tag or tag empty values")
	}
	s, local, ok := Split(id)
	if !ok || s != server || local != "abc" {
		t.Fatalf("Split = %q %q %v", s, local, ok)
	}
	if _, _, ok := Split("r-xyz-abc"); ok {
		t.Fatal("non-hex server must not split")
	}
}

func TestFindServerAndStrip(t *testing.T) {
	text := "/tasks/" + ID(server, "t1") + "/thread?project_id=" + ID(server, "p1")
	if s, ok := FindServer(text); !ok || s != server {
		t.Fatalf("FindServer = %q %v", s, ok)
	}
	if _, ok := FindServer("/tasks/xr-0123456789abcdef-1"); ok {
		t.Fatal("prefix inside a word must not match")
	}
	if got := Strip(server, text); got != "/tasks/t1/thread?project_id=p1" {
		t.Fatalf("Strip = %q", got)
	}
}
