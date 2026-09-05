package models

// BreadcrumbSelector configures a searchable selector for one selected deep resource.
type BreadcrumbSelector struct {
	ID          string
	Kind        string
	CurrentID   string
	CurrentName string
	SearchURL   string
}

// BreadcrumbSelectorItem is the compact, authoritative result rendered by a selector endpoint.
type BreadcrumbSelectorItem struct {
	ID   string
	Name string
	URL  string
}
