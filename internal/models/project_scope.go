package models

// AllProjectsID identifies the aggregate UI and its reserved chat-history owner.
// It is never a destination for project work.
const AllProjectsID = "__all_projects__"

func AllProjectsOption() Project {
	return Project{ID: AllProjectsID, Name: "All projects"}
}
