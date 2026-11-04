package models

import "time"

// PagePluginPackage is an installed, immutable plugin bundle version.
type PagePluginPackage struct {
	ID           string
	PluginID     string
	Version      string
	Name         string
	ContentHash  string
	ManifestJSON string
	InstallPath  string
	InstalledBy  string
	InstalledAt  time.Time
}

// PagePluginInstance is one page of a package enabled inside one project.
type PagePluginInstance struct {
	ID           string
	ProjectID    string
	PackageID    string
	PageID       string
	Title        string
	Enabled      bool
	ConfigJSON   string
	StateJSON    string
	ApprovedHash string
	ApprovedBy   string
	ApprovedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// Joined from the package for display and approval checks.
	PluginID    string
	Version     string
	ContentHash string
	ProjectName string
}
