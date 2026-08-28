package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type repositoryCache struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

type cachedWorktree struct {
	cache      *repositoryCache
	mirrorPath string
	path       string
}

func newRepositoryCache(root string) *repositoryCache {
	root = strings.TrimSpace(root)
	if root == "" {
		root = filepath.Join(os.TempDir(), "dispatch-repositories")
	}
	return &repositoryCache{root: filepath.Clean(root), locks: map[string]*sync.Mutex{}}
}

func (c *repositoryCache) checkout(ctx context.Context, repositoryURL, credentialID, branch, commit, destination string, environment []string) (*cachedWorktree, error) {
	key := repositoryCacheKey(repositoryURL, credentialID)
	unlock := c.lock(key)
	defer unlock()

	mirrorPath := filepath.Join(c.root, "mirrors", key+".git")
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(mirrorPath); errors.Is(err, os.ErrNotExist) {
		if err := runGit(ctx, environment, "init", "--bare", mirrorPath); err != nil {
			return nil, err
		}
		if err := runGit(ctx, environment, "--git-dir", mirrorPath, "remote", "add", "origin", repositoryURL); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := runGit(ctx, environment, "--git-dir", mirrorPath, "remote", "set-url", "origin", repositoryURL); err != nil {
		return nil, err
	}
	if err := runGit(ctx, environment, "--git-dir", mirrorPath, "cat-file", "-e", commit+"^{commit}"); err != nil {
		refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
		if fetchErr := runGit(ctx, environment, "--git-dir", mirrorPath, "fetch", "--no-tags", "--prune", "origin", refspec); fetchErr != nil {
			return nil, fmt.Errorf("fetch repository: %w", fetchErr)
		}
		if err := runGit(ctx, environment, "--git-dir", mirrorPath, "cat-file", "-e", commit+"^{commit}"); err != nil {
			return nil, fmt.Errorf("revision %s was not fetched from branch %s", commit, branch)
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return nil, err
	}
	_ = runGit(ctx, environment, "--git-dir", mirrorPath, "worktree", "prune")
	if err := runGit(ctx, environment, "--git-dir", mirrorPath, "worktree", "add", "--force", "--detach", destination, commit); err != nil {
		return nil, fmt.Errorf("create repository worktree: %w", err)
	}
	return &cachedWorktree{cache: c, mirrorPath: mirrorPath, path: destination}, nil
}

func (w *cachedWorktree) remove(ctx context.Context) error {
	if w == nil || w.cache == nil {
		return nil
	}
	key := strings.TrimSuffix(filepath.Base(w.mirrorPath), ".git")
	unlock := w.cache.lock(key)
	defer unlock()
	err := runGit(ctx, os.Environ(), "--git-dir", w.mirrorPath, "worktree", "remove", "--force", w.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *repositoryCache) lock(key string) func() {
	c.mu.Lock()
	lock := c.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		c.locks[key] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func repositoryCacheKey(repositoryURL, credentialID string) string {
	canonical := canonicalRepositoryURL(repositoryURL)
	digest := sha256.Sum256([]byte(canonical + "\x00" + strings.TrimSpace(credentialID)))
	return hex.EncodeToString(digest[:])
}

func canonicalRepositoryURL(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(strings.TrimSuffix(strings.TrimRight(value, "/"), ".git"))
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.User = nil
	parsed.RawQuery, parsed.Fragment = "", ""
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), ".git")
	return parsed.String()
}

func runGit(ctx context.Context, environment []string, args ...string) error {
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = environment, &output, &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			return err
		}
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return nil
}
