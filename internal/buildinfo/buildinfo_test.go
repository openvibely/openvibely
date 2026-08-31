package buildinfo

import "testing"

func TestCurrentUsesCommandDefaultWithoutRelabelingOverride(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime, oldArtifact := Version, Commit, BuildTime, Artifact
	t.Cleanup(func() { Version, Commit, BuildTime, Artifact = oldVersion, oldCommit, oldBuildTime, oldArtifact })
	Version, Commit, BuildTime, Artifact = "", "abc123", "", ""

	server := Current(ArtifactSource)
	if server.Artifact != ArtifactSource || server.Version != "dev-abc123" {
		t.Fatalf("server identity = %#v", server)
	}
	desktop := Current(ArtifactDesktop)
	if desktop.Artifact != ArtifactDesktop {
		t.Fatalf("desktop artifact = %q", desktop.Artifact)
	}

	Artifact = ArtifactBinary
	if got := Current(ArtifactSource).Artifact; got != ArtifactBinary {
		t.Fatalf("linker artifact override = %q", got)
	}
}

func TestResolveDistribution(t *testing.T) {
	tests := []struct{ artifact, mode, want string }{
		{ArtifactSource, ModeNone, DistributionSource},
		{ArtifactBinary, ModeNone, DistributionBinary},
		{ArtifactDesktop, ModeNone, DistributionDesktop},
		{ArtifactContainer, ModeHosted, DistributionHosted},
		{ArtifactContainer, ModeDockerAgent, DistributionDocker},
		{ArtifactContainer, ModeDockerManual, DistributionDocker},
	}
	for _, tt := range tests {
		got, err := ResolveDistribution(tt.artifact, tt.mode)
		if err != nil || got != tt.want {
			t.Fatalf("ResolveDistribution(%q, %q) = %q, %v; want %q", tt.artifact, tt.mode, got, err, tt.want)
		}
	}
	for _, tt := range [][2]string{{ArtifactSource, ModeHosted}, {ArtifactContainer, ModeNone}, {ArtifactContainer, "unknown"}, {"unknown", ModeNone}} {
		if _, err := ResolveDistribution(tt[0], tt[1]); err == nil {
			t.Fatalf("ResolveDistribution(%q, %q) succeeded", tt[0], tt[1])
		}
	}
}
