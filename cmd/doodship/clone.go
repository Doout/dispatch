package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func (r *runner) clonePRAlias(watched []string, explicitPR bool) (string, error) {
	if r.o.cloneProtocol == "" || r.o.pr == 0 {
		return "", nil
	}
	if len(watched) == 0 {
		watched = slices.Sorted(maps.Keys(r.doc.Spec.Sources))
	}
	if alias := r.o.prSource; alias != "" {
		if !slices.Contains(watched, alias) {
			return "", fmt.Errorf("--pr-source must be one of: %s", strings.Join(watched, ", "))
		}
		return alias, nil
	}
	if explicitPR {
		return "", nil // Explicit watched checkouts supply their committed HEAD.
	}
	if len(watched) == 1 {
		return watched[0], nil
	}
	return "", fmt.Errorf("choose the repository that owns PR %d with --pr-source (%s)", r.o.pr, strings.Join(watched, ", "))
}

var repoSlug = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)

func cloneURL(repository, host, protocol string) (string, error) {
	if host == "" {
		host = "github.com"
	}
	slug := repository
	if strings.HasPrefix(slug, "git@") && !strings.Contains(slug, "://") {
		var ok bool
		host, slug, ok = strings.Cut(strings.TrimPrefix(slug, "git@"), ":")
		if !ok {
			return "", errors.New("expected git@host:owner/repository")
		}
	} else if strings.Contains(repository, "://") {
		u, err := url.Parse(repository)
		if err != nil || (u.Scheme != "ssh" && u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.User != nil && (u.Scheme != "ssh" || u.User.String() != "git") || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("expected a Git repository URL without embedded credentials")
		}
		host, slug = u.Host, strings.TrimPrefix(u.Path, "/")
	}
	slug = strings.TrimSuffix(slug, ".git")
	if !repoSlug.MatchString(slug) || strings.HasSuffix(slug, "/.") || strings.HasSuffix(slug, "/..") {
		return "", errors.New("expected an owner/repository name or Git URL")
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || strings.HasPrefix(u.Hostname(), "-") || strings.ContainsAny(host, "/?#@%") {
		return "", errors.New("expected a Git host name with an optional port")
	}
	u.Scheme, u.Path = protocol, "/"+slug+".git"
	if protocol == "ssh" {
		u.User = url.User("git")
	}
	return u.String(), nil
}

func (r *runner) cloneSource(ctx context.Context, alias, repository, ref string) (string, error) {
	remote, err := cloneURL(repository, r.o.gitHost, r.o.cloneProtocol)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.root, ".git-"+alias)
	if _, err := git(ctx, r.root, "init", "--quiet", "--bare", "--", dir); err != nil {
		return "", err
	}
	if _, err := gitWithProtocol(ctx, dir, r.o.cloneProtocol, "fetch", "--quiet", "--no-recurse-submodules", "--", remote, ref); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", ref, remote, err)
	}
	_, err = git(ctx, dir, "update-ref", "HEAD", "FETCH_HEAD^{commit}")
	return dir, err
}
