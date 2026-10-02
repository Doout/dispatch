package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

// CheckSourceRepository refreshes evidence without changing the source name,
// branch, resource definitions or previously accepted commit records.
func (s *Service) CheckSourceRepository(ctx context.Context, id string) (core.ConfigSource, error) {
	unlock := s.lock("source:" + id)
	defer unlock()
	source, err := s.Store.GetConfigSource(ctx, id)
	if err != nil {
		return source, err
	}
	if source.GitHubAppID == "" || s.GitHub == nil {
		return source, errors.New("this source requires a GitHub App for repository identity checks")
	}
	status, err := s.GitHub.CheckRepository(ctx, source.GitHubAppID, source.Repository, source.RepositoryID)
	if err != nil {
		return source, err
	}
	previous := source.RepositoryStatus
	source.RepositoryStatus, source.UpdatedAt = &status, time.Now().UTC()
	if checkErr := githubapp.RequireAccessible(status); checkErr != nil {
		source.State, source.LastError = "invalid", checkErr.Error()
	} else {
		source.RepositoryID = status.RepositoryID
		// Access can recover while configuration errors remain. A full sync is
		// still required before publishing replacement application definitions.
		if previous != nil && previous.State != "accessible" {
			source.State, source.LastError = "pending", "Repository access restored. Sync to validate the configuration."
		}
	}
	return source, s.Store.UpdateConfigSource(ctx, source)
}

// UpdateSource serializes user edits with source synchronization in this controller.
func (s *Service) UpdateSource(ctx context.Context, source core.ConfigSource) error {
	unlock := s.lock("source:" + source.ID)
	defer unlock()
	return s.Store.UpdateConfigSource(ctx, source)
}

func (s *Service) requireRepositoryIdentity(ctx context.Context, source core.ConfigSource) error {
	if source.GitHubAppID == "" || source.RepositoryID == 0 {
		return nil
	}
	if s.GitHub == nil {
		return errors.New("GitHub App access is not configured")
	}
	status, err := s.GitHub.CheckRepository(ctx, source.GitHubAppID, source.Repository, source.RepositoryID)
	if err != nil {
		return err
	}
	return githubapp.RequireAccessible(status)
}
