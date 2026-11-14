package service

import "testing"

func TestProjectComputerRejectsLegacyImages(t *testing.T) {
	for _, p := range []ComputerProvisioner{&LocalComputerProvisioner{}, &HostedComputerProvisioner{}} {
		s := &ProjectComputerService{provisioner: p}
		for _, data := range []string{`{"ready":true}`, `{"isolation":0}`, `{"isolation":1}`, `not-json`} {
			if s.requireIsolation([]byte(data)) == nil {
				t.Fatalf("%s accepted legacy image %s", p.Provider(), data)
			}
		}
		if err := s.requireIsolation([]byte(`{"isolation":1,"viewer_connections":1}`)); err != nil {
			t.Fatal(err)
		}
	}
}
