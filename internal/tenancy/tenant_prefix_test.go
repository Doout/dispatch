package tenancy

import "testing"

func TestPreviewPrefixCannotBeATenant(t *testing.T) {
	for _, slug := range []string{"p1-team", "p223-abc-agentops", "p999999-team", "pr-legacy", "app-abc-team"} {
		if !ReservedTenantPrefix(slug) || ValidSlug(slug) {
			t.Fatalf("workload prefix was available to a tenant: %s", slug)
		}
	}
	for _, slug := range []string{"platform-team", "p-team", "p223", "p223team", "project-223", "panda"} {
		if ReservedTenantPrefix(slug) || !ValidSlug(slug) {
			t.Fatalf("ordinary tenant name was reserved: %s", slug)
		}
	}
}
