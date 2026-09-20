package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func (a *API) githubMappedGroups(ctx context.Context, client *http.Client, provider core.AuthProvider, token string) ([]string, error) {
	data, ok := a.store.(interface {
		ListIdentityTeamMappings(context.Context) ([]core.IdentityTeamMapping, error)
	})
	if !ok {
		return nil, nil
	}
	mappings, err := data.ListIdentityTeamMappings(ctx)
	if err != nil {
		return nil, err
	}
	needed := false
	for _, m := range mappings {
		needed = needed || m.ProviderID == provider.ID
	}
	if !needed {
		return nil, nil
	}
	groups := []string{}
	for page := 1; page <= 20; page++ {
		var teams []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		if err := githubOAuthGet(ctx, client, fmt.Sprintf("%s/user/teams?per_page=100&page=%d", provider.APIURL, page), token, &teams); err != nil {
			return nil, errors.New("provider team membership could not be verified")
		}
		for _, t := range teams {
			if t.Slug != "" && t.Organization.Login != "" {
				groups = append(groups, strings.ToLower(t.Organization.Login+"/"+t.Slug))
			}
		}
		if len(teams) < 100 {
			return groups, nil
		}
	}
	return nil, errors.New("provider team membership exceeded the verification limit")
}
func (a *API) syncLoginTeams(ctx context.Context, provider, user string, groups []string) error {
	if data, ok := a.store.(interface {
		SyncIdentityTeamMemberships(context.Context, string, string, []string) error
	}); ok {
		return data.SyncIdentityTeamMemberships(ctx, provider, user, groups)
	}
	return nil
}
