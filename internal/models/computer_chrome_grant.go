package models

type ChromeCookieSelection struct {
	Profile string `json:"profile"`
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	Path    string `json:"path"`
}
type ComputerChromeGrant struct {
	ID         string                  `json:"id"`
	ProjectID  string                  `json:"project_id"`
	TaskID     string                  `json:"task_id"`
	Scope      string                  `json:"scope"`
	Selections []ChromeCookieSelection `json:"selections"`
	ExpiresAt  int64                   `json:"expires_at"`
	OneTime    bool                    `json:"one_time"`
	Revoked    bool                    `json:"revoked"`
}
