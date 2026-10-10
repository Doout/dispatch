// Package dnsprovider publishes records through an operator-selected DNS service.
package dnsprovider

import "context"

// Record identifies one Dispatch-owned set of values. ID must remain stable
// across retries and distinct from other owners at the same name.
type Record struct {
	ID, Name, Type string
	Values         []string
	TTL            uint32
}

// Provider operations are safe to retry after an interrupted request. They must
// not adopt or remove records belonging to another owner.
type Provider interface {
	// Target identifies the provider's account or zone without credentials. It
	// stays unchanged when credentials rotate and differs between destinations.
	Target() string
	Ensure(context.Context, Record) error
	Delete(context.Context, Record) error
	Nameservers(context.Context) ([]string, error)
}
