package core

// DeploymentSearch is scoped to an explicit set of authorized applications.
type DeploymentSearch struct {
	AppIDs   []string
	Before   string
	Query    string
	State    string
	Revision string
	Limit    int
}
