package config

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestValidateUpdateConfigurationArtifactModeMatrix(t *testing.T) {
	base := func() *Config {
		return &Config{Mode: ModeServer, BuildArtifact: buildinfo.ArtifactContainer, UpdateMode: buildinfo.ModeDockerManual, UpdateServiceURL: "https://openvibely.ai", UpdateChannel: "stable"}
	}
	if err := base().ValidateUpdate(); err != nil {
		t.Fatal(err)
	}

	bad := base()
	bad.BuildArtifact = buildinfo.ArtifactSource
	bad.UpdateMode = buildinfo.ModeHosted
	if err := bad.ValidateUpdate(); err == nil {
		t.Fatal("source build accepted hosted mode")
	}

	hosted := base()
	hosted.UpdateMode = buildinfo.ModeHosted
	hosted.HostedSSOControlURL = "https://control.openvibely.ai"
	hosted.HostedSSOInstanceID = "instance-1"
	if err := hosted.ValidateUpdate(); err == nil || !strings.Contains(err.Error(), "OPENVIBELY_HOSTED_AGENT_TOKEN") {
		t.Fatalf("hosted validation error = %v", err)
	}

	agent := base()
	agent.UpdateMode = buildinfo.ModeDockerAgent
	if err := agent.ValidateUpdate(); err != nil {
		t.Fatalf("docker-agent missing config failed startup: %v", err)
	}
	if agent.ManagedUpdateError == "" {
		t.Fatal("docker-agent configuration error not exposed")
	}
}

func TestValidateUpdateConfigurationAllowsPackagedBinaryWithoutServiceManager(t *testing.T) {
	cfg := &Config{
		Mode:             ModeServer,
		BuildArtifact:    buildinfo.ArtifactBinary,
		UpdateMode:       buildinfo.ModeNone,
		UpdateServiceURL: "https://openvibely.ai",
		UpdateChannel:    "stable",
	}
	if err := cfg.ValidateUpdate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ManagedUpdateError != "" {
		t.Fatalf("packaged binary update error = %q", cfg.ManagedUpdateError)
	}
}

func TestPackagedUpdateNotificationsDefaultOffWhileChecksRemainEnabled(t *testing.T) {
	originalArtifact := buildinfo.Artifact
	t.Cleanup(func() { buildinfo.Artifact = originalArtifact })

	for _, name := range []string{"DISABLE_UPDATE_NOTIFICATIONS", "OPENVIBELY_UPDATE_MODE"} {
		t.Setenv(name, "")
	}

	for _, test := range []struct {
		name                 string
		mode                 RuntimeMode
		artifact             string
		wantNotificationsOff bool
	}{
		{name: "source metrics unchanged", mode: ModeServer, artifact: buildinfo.ArtifactSource, wantNotificationsOff: false},
		{name: "standalone binary offers default off", mode: ModeServer, artifact: buildinfo.ArtifactBinary, wantNotificationsOff: true},
		{name: "desktop offers default off", mode: ModeDesktop, artifact: buildinfo.ArtifactDesktop, wantNotificationsOff: true},
		{name: "container updates unchanged", mode: ModeServer, artifact: buildinfo.ArtifactContainer, wantNotificationsOff: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			buildinfo.Artifact = test.artifact
			cfg := LoadWithMode(test.mode)
			if cfg.DisableUpdateNotifications != test.wantNotificationsOff {
				t.Fatalf("DisableUpdateNotifications=%v want %v for artifact %q", cfg.DisableUpdateNotifications, test.wantNotificationsOff, test.artifact)
			}
		})
	}

	buildinfo.Artifact = buildinfo.ArtifactBinary
	t.Setenv("DISABLE_UPDATE_NOTIFICATIONS", "false")
	if cfg := LoadWithMode(ModeServer); cfg.DisableUpdateNotifications {
		t.Fatal("explicit DISABLE_UPDATE_NOTIFICATIONS=false did not enable packaged update offers")
	}
}

func TestValidateUpdateServiceURL(t *testing.T) {
	for _, good := range []string{"https://openvibely.ai", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		c := &Config{BuildArtifact: buildinfo.ArtifactSource, UpdateMode: buildinfo.ModeNone, UpdateServiceURL: good}
		if err := c.ValidateUpdate(); err != nil {
			t.Fatalf("%s: %v", good, err)
		}
	}
	for _, badURL := range []string{"http://example.com", "https://example.com/path", "file:///tmp/update"} {
		c := &Config{BuildArtifact: buildinfo.ArtifactSource, UpdateMode: buildinfo.ModeNone, UpdateServiceURL: badURL}
		if err := c.ValidateUpdate(); err == nil {
			t.Fatalf("accepted %s", badURL)
		}
	}
}
