package service

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

const (
	maintainedVisionSuggestionsNodeKey = "vision_suggestions"
	rootVisionSourceName               = "VISION.md"
	maxRootVisionSourceBytes           = 512 * 1024

	visionSourceFoundMessage      = "Vision source found: root VISION.md"
	visionSourceMissingMessage    = "Vision Suggestions can run, but no root VISION.md was found; add one or customize the prompt/source before enabling this schedule."
	visionSourceUnreadableMessage = "Vision Suggestions can run, but root VISION.md could not be read; add a readable source or customize the prompt before enabling this schedule."
	visionSourceNoRepoMessage     = "Vision Suggestions can run, but this project has no repository path, so root VISION.md could not be checked; add a vision source or customize the prompt before enabling this schedule."
	visionSourceTruncatedMessage  = "Vision source found: root VISION.md (truncated for preview; the file is larger than 512 KiB)."
)

const (
	visionSourceFound      = "found"
	visionSourceMissing    = "missing"
	visionSourceUnreadable = "unreadable"
	visionSourceNoRepo     = "no_repo"
	visionSourceTruncated  = "truncated"
)

type VisionSourceStatus struct {
	State   string
	Path    string
	Message string
}

func InspectRootVisionSource(repoPath string) VisionSourceStatus {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return VisionSourceStatus{State: visionSourceNoRepo, Message: visionSourceNoRepoMessage}
	}
	root, err := os.OpenRoot(repoPath)
	if err != nil {
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	defer root.Close()
	info, err := root.Lstat(rootVisionSourceName)
	if err != nil {
		if os.IsNotExist(err) {
			return VisionSourceStatus{State: visionSourceMissing, Path: rootVisionSourceName, Message: visionSourceMissingMessage}
		}
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	file, err := openRootVisionSource(root)
	if err != nil {
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	read, err := io.CopyN(io.Discard, file, maxRootVisionSourceBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return VisionSourceStatus{State: visionSourceUnreadable, Path: rootVisionSourceName, Message: visionSourceUnreadableMessage}
	}
	if read > maxRootVisionSourceBytes {
		return VisionSourceStatus{State: visionSourceTruncated, Path: rootVisionSourceName, Message: visionSourceTruncatedMessage}
	}
	return VisionSourceStatus{State: visionSourceFound, Path: rootVisionSourceName, Message: visionSourceFoundMessage}
}

func maintainedSDLCHasVisionSuggestions(candidate models.AutomationDraftCandidate) bool {
	switch strings.TrimSpace(candidate.AdapterKey) {
	case AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC:
	default:
		return false
	}
	for _, node := range candidate.Nodes {
		if strings.TrimSpace(node.Key) == maintainedVisionSuggestionsNodeKey {
			return true
		}
	}
	return false
}

func isMaintainedVisionSourceMessage(value string) bool {
	switch strings.TrimSpace(value) {
	case visionSourceFoundMessage, visionSourceMissingMessage, visionSourceUnreadableMessage, visionSourceNoRepoMessage, visionSourceTruncatedMessage:
		return true
	default:
		return false
	}
}

func stripMaintainedVisionSourceMessages(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if isMaintainedVisionSourceMessage(value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func AnnotateMaintainedSDLCVisionSource(candidate models.AutomationDraftCandidate, repoPath string) models.AutomationDraftCandidate {
	candidate.Assumptions = stripMaintainedVisionSourceMessages(candidate.Assumptions)
	candidate.Warnings = stripMaintainedVisionSourceMessages(candidate.Warnings)
	if !maintainedSDLCHasVisionSuggestions(candidate) {
		candidate.Assumptions = normalizeDraftMessages(candidate.Assumptions)
		candidate.Warnings = normalizeDraftMessages(candidate.Warnings)
		return candidate
	}
	status := InspectRootVisionSource(repoPath)
	switch status.State {
	case visionSourceFound, visionSourceTruncated:
		candidate.Assumptions = append(candidate.Assumptions, status.Message)
	default:
		candidate.Warnings = append(candidate.Warnings, status.Message)
	}
	candidate.Assumptions = normalizeDraftMessages(candidate.Assumptions)
	candidate.Warnings = normalizeDraftMessages(candidate.Warnings)
	return candidate
}
