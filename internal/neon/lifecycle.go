package neon

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Progress persists the provider phase through the existing owned-service run.
// A failed callback stops before the next external mutation.
type Progress func(context.Context, string, Resource) error

func report(ctx context.Context, p Progress, phase string, r Resource) error {
	if p != nil {
		return p(ctx, phase, r)
	}
	return nil
}

// Ensure adopts a matching branch and completes missing compute/role setup.
// Creation is allowed only for an accepted operation whose durable state permits it.
func (c *Client) Ensure(ctx context.Context, s Spec, scope Scope, allowCreate bool, progress Progress) (Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := validateScope(s, scope); err != nil {
		return Resource{}, err
	}
	found, err := c.Find(ctx, s, scope)
	if err != nil {
		return Resource{}, err
	}
	if found == nil {
		if !allowCreate {
			return Resource{State: "absent"}, errors.New("Neon branch is absent; inspect and explicitly retry the accepted operation")
		}
		var parent branchResponse
		if err = c.request(ctx, "GET", projectPath(s)+"/branches/"+s.ParentBranchID, nil, nil, &parent); err != nil {
			return Resource{}, err
		}
		if parent.Branch.ID != s.ParentBranchID || parent.Branch.ProjectID != s.ProjectID {
			return Resource{}, errors.New("Neon parent branch ownership changed")
		}
		// Never return a role inherited from the source branch as a preview credential.
		if err = c.ensureRoleNotInherited(ctx, s, scope); err != nil {
			return Resource{}, err
		}
		if err = report(ctx, progress, "Creating isolated Neon branch", Resource{State: "creating"}); err != nil {
			return Resource{}, err
		}
		mode := s.DataMode
		if mode == "" {
			mode = "schema-only"
		}
		branch := map[string]any{"parent_id": s.ParentBranchID, "name": Name(scope), "init_source": mode}
		endpoint := map[string]any{"type": "read_write"}
		if s.SuspendAfterSeconds > 0 {
			endpoint["suspend_timeout_seconds"] = s.SuspendAfterSeconds
		}
		body := map[string]any{"branch": branch, "endpoints": []any{endpoint}, "annotation_value": annotations(scope)}
		var created branchResponse
		if err = c.request(ctx, "POST", projectPath(s)+"/branches", nil, body, &created); err != nil {
			return Resource{}, err
		}
		if !providerID.MatchString(created.Branch.ID) || created.Branch.ProjectID != s.ProjectID {
			return Resource{}, errors.New("Neon create response has no owned branch identity")
		}
		if err = report(ctx, progress, "Neon branch created", Resource{Branch: created.Branch, State: "creating"}); err != nil {
			return Resource{}, err
		}
		b, err := c.branch(ctx, s, scope, created.Branch.ID)
		if err != nil {
			return Resource{Branch: created.Branch}, err
		}
		found = &b
	}
	result := Resource{Branch: *found, Role: Role(scope), State: "unready"}
	if err = c.ensureRoleNotInherited(ctx, s, scope); err != nil {
		return result, err
	}
	var endpoints struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	if err = c.request(ctx, "GET", projectPath(s)+"/branches/"+found.ID+"/endpoints", nil, nil, &endpoints); err != nil {
		return result, err
	}
	for _, ep := range endpoints.Endpoints {
		if ep.Type == "read_write" {
			if result.Endpoint.ID != "" || ep.BranchID != found.ID || ep.ProjectID != s.ProjectID || !providerID.MatchString(ep.ID) {
				return result, errors.New("Neon compute ownership is ambiguous")
			}
			result.Endpoint = ep
		}
	}
	if result.Endpoint.ID == "" {
		if err = report(ctx, progress, "Creating Neon read-write compute", result); err != nil {
			return result, err
		}
		value := map[string]any{"branch_id": found.ID, "type": "read_write"}
		if s.SuspendAfterSeconds > 0 {
			value["suspend_timeout_seconds"] = s.SuspendAfterSeconds
		}
		var response struct {
			Endpoint Endpoint `json:"endpoint"`
		}
		if err = c.request(ctx, "POST", projectPath(s)+"/endpoints", nil, map[string]any{"endpoint": value}, &response); err != nil {
			return result, err
		}
	}
	var roleResponse struct {
		Role role `json:"role"`
	}
	rolePath := projectPath(s) + "/branches/" + found.ID + "/roles/" + url.PathEscape(result.Role)
	err = c.request(ctx, "GET", rolePath, nil, nil, &roleResponse)
	if IsNotFound(err) {
		if err = report(ctx, progress, "Creating branch-specific database role", result); err != nil {
			return result, err
		}
		err = c.request(ctx, "POST", projectPath(s)+"/branches/"+found.ID+"/roles", nil, map[string]any{"role": map[string]any{"name": result.Role}}, &roleResponse)
	}
	if err != nil {
		return result, err
	}
	if roleResponse.Role.Name != result.Role || roleResponse.Role.BranchID != found.ID || roleResponse.Role.Protected {
		return result, errors.New("Neon role does not match the owned branch")
	}
	if err = report(ctx, progress, "Waiting for Neon branch and compute readiness", result); err != nil {
		return result, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := c.Inspect(ctx, s, scope)
		if err != nil {
			return result, err
		}
		if current.State == "ready" {
			if err = report(ctx, progress, "Neon database is ready", current); err != nil {
				return current, err
			}
			return current, nil
		}
		if current.State == "absent" {
			return result, errors.New("owned Neon branch disappeared while provisioning")
		}
		select {
		case <-ctx.Done():
			return result, &APIError{Action: "provision", Uncertain: true}
		case <-ticker.C:
		}
	}
}
func (c *Client) ensureRoleNotInherited(ctx context.Context, s Spec, scope Scope) error {
	var existing struct {
		Role role `json:"role"`
	}
	err := c.request(ctx, "GET", projectPath(s)+"/branches/"+s.ParentBranchID+"/roles/"+url.PathEscape(Role(scope)), nil, nil, &existing)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("branch-specific Neon role already exists on the source; production credentials cannot be reused")
}
func (c *Client) Connection(ctx context.Context, s Spec, scope Scope, expected string) (map[string]string, error) {
	current, err := c.Inspect(ctx, s, scope)
	if err != nil {
		return nil, err
	}
	if current.State != "ready" || current.Branch.ID != expected {
		return nil, errors.New("Neon connection requires the exact ready owned branch")
	}
	if err = c.ensureRoleNotInherited(ctx, s, scope); err != nil {
		return nil, err
	}
	query := url.Values{"branch_id": {current.Branch.ID}, "endpoint_id": {current.Endpoint.ID}, "database_name": {s.Database}, "role_name": {current.Role}, "pooled": {"false"}}
	var response struct {
		URI string `json:"uri"`
	}
	if err = c.request(ctx, "GET", projectPath(s)+"/connection_uri", query, nil, &response); err != nil {
		return nil, err
	}
	u, err := url.Parse(response.URI)
	if err != nil || u.Scheme != "postgresql" && u.Scheme != "postgres" || u.User == nil || u.User.Username() != current.Role || u.Hostname() != current.Endpoint.Host || strings.TrimPrefix(u.Path, "/") != s.Database || u.Fragment != "" {
		return nil, errors.New("Neon connection does not match the owned role, branch compute or database")
	}
	for key, values := range u.Query() {
		if (key != "sslmode" && key != "channel_binding") || len(values) != 1 {
			return nil, errors.New("Neon connection contains unsupported connection overrides")
		}
	}
	if u.Port() != "" && u.Port() != "5432" {
		return nil, errors.New("Neon connection has an unexpected database port")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return nil, errors.New("Neon branch-specific password is unavailable")
	}
	ssl := u.Query().Get("sslmode")
	if ssl != "require" && ssl != "verify-full" && ssl != "verify-ca" {
		return nil, errors.New("Neon connection must require TLS")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return map[string]string{"connectionUrl": u.String(), "host": u.Hostname(), "port": port, "database": s.Database, "username": current.Role, "password": password, "sslmode": ssl}, nil
}

// Delete removes only the reviewed owned branch and waits for confirmed absence.
// It does not follow parent/default branches or delete a project.
func (c *Client) Delete(ctx context.Context, s Spec, scope Scope, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := validateScope(s, scope); err != nil {
		return err
	}
	b, err := c.branch(ctx, s, scope, expected)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.ID == s.ParentBranchID {
		return errors.New("Neon source branch cannot be deleted")
	}
	if err = c.request(ctx, "DELETE", projectPath(s)+"/branches/"+b.ID, nil, nil, nil); err != nil && !IsNotFound(err) {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err = c.branch(ctx, s, scope, b.ID)
		if IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return &APIError{Action: "delete", Uncertain: true}
		case <-ticker.C:
		}
	}
}
func (c *Client) Suspend(ctx context.Context, s Spec, scope Scope, expected string) error {
	current, err := c.Inspect(ctx, s, scope)
	if err != nil {
		return err
	}
	if current.Branch.ID != expected || current.Endpoint.ID == "" {
		return errors.New("Neon compute no longer matches the reviewed branch")
	}
	return c.request(ctx, "POST", projectPath(s)+"/endpoints/"+current.Endpoint.ID+"/suspend", nil, nil, nil)
}
