// Package tenancy keeps hosted identity and tenant metadata separate from each
// tenant's operational database. Platform administration never grants membership.
package tenancy

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound  = errors.New("tenant record not found")
	ErrDenied    = errors.New("tenant access denied")
	ErrConflict  = errors.New("tenant record already exists")
	ErrLastOwner = errors.New("the tenant must retain an active owner")
	ErrInvalid   = errors.New("invalid tenant input")
	ErrExpired   = errors.New("credential expired or revoked")
)

const (
	RoleOwner        = "owner"
	RoleAdmin        = "admin"
	RoleMember       = "member"
	StateActive      = "active"
	StateDisabled    = "disabled"
	StatePending     = "pending"
	AudiencePlatform = "platform"
)

func TenantAudience(id string) string { return "tenant:" + id }

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	PlatformAdmin bool      `json:"platformAdmin"`
	EmailVerified bool      `json:"emailVerified"`
	PasswordHash  string    `json:"-"`
	Version       int64     `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Tenant struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

type Membership struct {
	TenantID  string    `json:"tenantId"`
	UserID    string    `json:"userId"`
	Role      string    `json:"role"`
	State     string    `json:"state"`
	Version   int64     `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateTenantInput struct {
	Slug            string
	Name            string
	InitialOwnerID  string
	InvitationEmail string
	CreatorID       string
}

// Usage contains aggregate measurements only. Workload names and identifiers,
// logs, repositories, configuration and member identities do not belong here.
type Usage struct {
	TenantID       string    `json:"tenantId"`
	Measured       []string  `json:"measured"`
	PeriodStart    time.Time `json:"periodStart"`
	PeriodEnd      time.Time `json:"periodEnd"`
	Projects       int64     `json:"projects"`
	Applications   int64     `json:"applications"`
	Members        int64     `json:"members"`
	Builds         int64     `json:"builds"`
	Deployments    int64     `json:"deployments"`
	BuildSeconds   int64     `json:"buildSeconds"`
	RuntimeSeconds int64     `json:"runtimeSeconds"`
	StorageBytes   int64     `json:"storageBytes"`
	TransferBytes  int64     `json:"transferBytes"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ZoneRecord struct {
	ID         string   `json:"id"`
	OwnerID    string   `json:"ownerId"`
	TenantID   string   `json:"tenantId"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Values     []string `json:"values"`
	TTL        uint32   `json:"ttl"`
	Generation uint64   `json:"generation"`
}

type Domain struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Hostname  string    `json:"hostname"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	ClaimHash string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

type ProvisioningJob struct {
	TenantID  string    `json:"tenantId"`
	State     string    `json:"state"`
	Phase     string    `json:"phase"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
