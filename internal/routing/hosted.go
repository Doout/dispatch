package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// HostedHostname keeps every workload one label below the installation root.
// The ID includes tenant and application ownership, so equal PR numbers in
// different repositories or projects cannot claim the same host.
func HostedHostname(scope core.HostedRoutingScope, app core.App) (string, error) {
	root, err := NormalizeHostname(scope.RootDomain)
	if err != nil || scope.TenantID == "" || !label.MatchString(scope.TenantSlug) || app.ID == "" || app.ProjectID == "" {
		return "", errors.New("hosted routing requires tenant and application ownership")
	}
	prefix := "app-"
	if app.Generated && len(app.HelmProvenance.PullRequests) > 0 && app.HelmProvenance.PullRequests[0].Number > 0 {
		prefix = "p" + strconv.Itoa(app.HelmProvenance.PullRequests[0].Number) + "-"
	}
	sum := sha256.Sum256([]byte(scope.TenantID + ":" + app.ProjectID + ":" + app.ID))
	identifier := hex.EncodeToString(sum[:8])
	slug := scope.TenantSlug
	limit := 63 - len(prefix) - len(identifier) - 1
	if len(slug) > limit {
		slug = strings.TrimRight(slug[:limit], "-")
	}
	return NormalizeHostname(prefix + identifier + "-" + slug + "." + root)
}
