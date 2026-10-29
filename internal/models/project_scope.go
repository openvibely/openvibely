package models

// AllProjectsID is a UI read scope, never a persisted project or a write target.
const AllProjectsID = "__all_projects__"

func AllProjectsOption() Project {
	return Project{ID: AllProjectsID, Name: "All projects"}
}
