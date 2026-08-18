package relay

import "time"

// Hook is a provider-neutral public webhook endpoint owned by one controller.
type Hook struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

// Delivery preserves the original webhook request. Provider-specific signature
// verification and parsing happen in Dispatch, never on the relay.
type Delivery struct {
	ID         string              `json:"id"`
	HookID     string              `json:"hookId"`
	Sequence   int64               `json:"sequence"`
	Method     string              `json:"method"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
	ReceivedAt time.Time           `json:"receivedAt"`
	Attempt    int                 `json:"attempt"`
	LeaseToken string              `json:"leaseToken"`
}

type Status struct {
	Pending       int        `json:"pending"`
	OldestPending *time.Time `json:"oldestPendingAt,omitempty"`
}

type hookRequest struct {
	Name string `json:"name"`
}

type ackRequest struct {
	LeaseToken  string `json:"leaseToken"`
	Disposition string `json:"disposition"`
	Error       string `json:"error,omitempty"`
}
