// Package buildinfo exposes immutable build-time identity shared by every command.
package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	ArtifactSource    = "source"
	ArtifactBinary    = "binary"
	ArtifactDesktop   = "desktop"
	ArtifactContainer = "container"

	ModeNone         = "none"
	ModeHosted       = "hosted"
	ModeDockerAgent  = "docker-agent"
	ModeDockerManual = "docker-manual"

	DistributionSource  = "source"
	DistributionBinary  = "binary"
	DistributionDesktop = "desktop"
	DistributionHosted  = "hosted"
	DistributionDocker  = "docker"
)

// These values are set only by -ldflags -X in official build pipelines.
var (
	Version          string
	Commit           string
	BuildTime        string
	Artifact         string
	ReleaseKeyID     string
	ReleasePublicKey string
)

type Build struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Artifact  string `json:"artifact"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current returns the compiled identity. defaultArtifact is command-owned and is
// used only when an official pipeline did not inject Artifact.
func Current(defaultArtifact string) Build {
	artifact := Artifact
	if artifact == "" {
		artifact = defaultArtifact
	}
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		commit = "unknown"
	}
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "dev-" + commit
	}
	buildTime := strings.TrimSpace(BuildTime)
	if buildTime == "" {
		buildTime = "1970-01-01T00:00:00Z"
	}
	return Build{Version: version, Commit: commit, BuildTime: buildTime, Artifact: artifact, OS: runtime.GOOS, Arch: runtime.GOARCH}
}

func ResolveDistribution(artifact, mode string) (string, error) {
	switch artifact {
	case ArtifactSource:
		if mode == ModeNone {
			return DistributionSource, nil
		}
	case ArtifactBinary:
		if mode == ModeNone {
			return DistributionBinary, nil
		}
	case ArtifactDesktop:
		if mode == ModeNone {
			return DistributionDesktop, nil
		}
	case ArtifactContainer:
		switch mode {
		case ModeHosted:
			return DistributionHosted, nil
		case ModeDockerAgent, ModeDockerManual:
			return DistributionDocker, nil
		}
	default:
		return "", fmt.Errorf("unknown build artifact %q", artifact)
	}
	return "", fmt.Errorf("update mode %q is incompatible with artifact %q", mode, artifact)
}
