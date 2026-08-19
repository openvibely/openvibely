package config

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestLoadWithModeUpdateDefaults(t *testing.T) {
	originalArtifact := buildinfo.Artifact
	t.Cleanup(func() { buildinfo.Artifact = originalArtifact })
	buildinfo.Artifact = ""

	for _, name := range []string{"OPENVIBELY_UPDATE_MODE", "OPENVIBELY_UPDATE_SERVICE_URL", "OPENVIBELY_UPDATE_CHANNEL"} {
		t.Setenv(name, "")
	}

	for _, test := range []struct {
		name         string
		mode         RuntimeMode
		wantArtifact string
	}{
		{name: "server", mode: ModeServer, wantArtifact: buildinfo.ArtifactSource},
		{name: "desktop", mode: ModeDesktop, wantArtifact: buildinfo.ArtifactDesktop},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := LoadWithMode(test.mode)
			if cfg.BuildArtifact != test.wantArtifact {
				t.Fatalf("BuildArtifact=%q want %q", cfg.BuildArtifact, test.wantArtifact)
			}
			if cfg.UpdateMode != buildinfo.ModeNone {
				t.Fatalf("UpdateMode=%q want %q", cfg.UpdateMode, buildinfo.ModeNone)
			}
			if cfg.UpdateServiceURL != defaultUpdateServiceURL {
				t.Fatalf("UpdateServiceURL=%q want %q", cfg.UpdateServiceURL, defaultUpdateServiceURL)
			}
			if cfg.UpdateChannel != defaultUpdateChannel {
				t.Fatalf("UpdateChannel=%q want %q", cfg.UpdateChannel, defaultUpdateChannel)
			}
		})
	}
}

func TestUpdateDefaultsUseDockerManualForContainers(t *testing.T) {
	originalArtifact := buildinfo.Artifact
	t.Cleanup(func() { buildinfo.Artifact = originalArtifact })
	buildinfo.Artifact = buildinfo.ArtifactContainer

	for _, name := range []string{"OPENVIBELY_UPDATE_MODE", "OPENVIBELY_UPDATE_SERVICE_URL", "OPENVIBELY_UPDATE_CHANNEL"} {
		t.Setenv(name, "")
	}

	cfg := LoadWithMode(ModeServer)
	if cfg.BuildArtifact != buildinfo.ArtifactContainer {
		t.Fatalf("BuildArtifact=%q want %q", cfg.BuildArtifact, buildinfo.ArtifactContainer)
	}
	if cfg.UpdateMode != buildinfo.ModeDockerManual {
		t.Fatalf("UpdateMode=%q want %q", cfg.UpdateMode, buildinfo.ModeDockerManual)
	}

	partial := (&Config{Mode: ModeServer, BuildArtifact: buildinfo.ArtifactContainer}).NormalizeForMode()
	if partial.UpdateMode != buildinfo.ModeDockerManual {
		t.Fatalf("NormalizeForMode UpdateMode=%q want %q", partial.UpdateMode, buildinfo.ModeDockerManual)
	}
}

func TestNormalizeForModeAndValidateUpdateSharePartialUpdateDefaults(t *testing.T) {
	originalArtifact := buildinfo.Artifact
	t.Cleanup(func() { buildinfo.Artifact = originalArtifact })
	buildinfo.Artifact = ""

	normalized := (&Config{Mode: ModeServer}).NormalizeForMode()
	validated := &Config{Mode: ModeServer}
	if err := validated.ValidateUpdate(); err != nil {
		t.Fatal(err)
	}

	if normalized.BuildArtifact != validated.BuildArtifact ||
		normalized.UpdateMode != validated.UpdateMode ||
		normalized.UpdateServiceURL != validated.UpdateServiceURL ||
		normalized.UpdateChannel != validated.UpdateChannel {
		t.Fatalf("NormalizeForMode defaults %#v differ from ValidateUpdate defaults %#v", normalized, validated)
	}
}

func TestUpdateDefaultsPreserveExplicitEnvAndStructValues(t *testing.T) {
	originalArtifact := buildinfo.Artifact
	t.Cleanup(func() { buildinfo.Artifact = originalArtifact })
	buildinfo.Artifact = buildinfo.ArtifactContainer

	t.Setenv("OPENVIBELY_UPDATE_MODE", " docker-agent ")
	t.Setenv("OPENVIBELY_UPDATE_SERVICE_URL", "http://localhost:8080")
	t.Setenv("OPENVIBELY_UPDATE_CHANNEL", "beta")

	loaded := LoadWithMode(ModeServer)
	if loaded.BuildArtifact != buildinfo.ArtifactContainer {
		t.Fatalf("BuildArtifact=%q want %q", loaded.BuildArtifact, buildinfo.ArtifactContainer)
	}
	if loaded.UpdateMode != buildinfo.ModeDockerAgent {
		t.Fatalf("UpdateMode=%q want explicit %q", loaded.UpdateMode, buildinfo.ModeDockerAgent)
	}
	if loaded.UpdateServiceURL != "http://localhost:8080" {
		t.Fatalf("UpdateServiceURL=%q want explicit localhost origin", loaded.UpdateServiceURL)
	}
	if loaded.UpdateChannel != "beta" {
		t.Fatalf("UpdateChannel=%q want explicit beta", loaded.UpdateChannel)
	}

	configured := &Config{
		Mode:             ModeDesktop,
		BuildArtifact:    buildinfo.ArtifactBinary,
		UpdateMode:       buildinfo.ModeNone,
		UpdateServiceURL: "http://localhost:9090",
		UpdateChannel:    "preview",
	}
	configured.NormalizeForMode()
	if configured.BuildArtifact != buildinfo.ArtifactBinary ||
		configured.UpdateMode != buildinfo.ModeNone ||
		configured.UpdateServiceURL != "http://localhost:9090" ||
		configured.UpdateChannel != "preview" {
		t.Fatalf("NormalizeForMode overwrote explicit update fields: %#v", configured)
	}
	if err := configured.ValidateUpdate(); err != nil {
		t.Fatal(err)
	}
	if configured.BuildArtifact != buildinfo.ArtifactBinary ||
		configured.UpdateMode != buildinfo.ModeNone ||
		configured.UpdateServiceURL != "http://localhost:9090" ||
		configured.UpdateChannel != "preview" {
		t.Fatalf("ValidateUpdate overwrote explicit update fields: %#v", configured)
	}
}

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
