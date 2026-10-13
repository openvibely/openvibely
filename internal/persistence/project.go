package persistence

type ProjectRepoRoot struct {
	ID       string
	RepoPath string
}

type ProjectRepoValidationProject struct {
	ID       string
	Name     string
	RepoPath string
	RepoURL  string
}
