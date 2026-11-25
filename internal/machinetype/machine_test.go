package machinetype

import "testing"

func TestHardwareClassification(t *testing.T) {
	for _, c := range []struct{ product, chassis, want string }{{"Dell", "9", "laptop"}, {"PC", "3", "desktop"}, {"Amazon EC2", "3", "cloud"}, {"VMware", "9", "cloud"}, {"Rack", "23", "server"}, {"unknown", "", "server"}} {
		if got := linux(c.product, c.chassis); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	for _, c := range []struct{ model, want string }{{"MacBookPro18,1", "laptop"}, {"Mac mini", "desktop"}, {"Mac Studio", "desktop"}, {"iMac19,1", "desktop"}, {"unknown", "server"}} {
		if got := apple(c.model); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	if Normalize("") != "server" || Normalize("<script>") != "server" {
		t.Fatal("invalid peer type must fall back")
	}
}
