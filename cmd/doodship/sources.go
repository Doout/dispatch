package main

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/doout/dispatch/internal/core"
	wf "github.com/doout/dispatch/internal/workflowfile"
)

var aliasName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,51}$`)

type checkout struct {
	repo, dir string
	revision  core.WorkflowSourceRevision
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitWithProtocol(ctx, dir, "file", args...)
}

func gitWithProtocol(ctx context.Context, dir, protocol string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL="+protocol, "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

// Accept SSH, scp-style and HTTPS origins for the same owner/repository.
func identity(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		remote = u.Path
	} else if _, path, ok := strings.Cut(remote, ":"); ok {
		remote = path
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(remote, "/"), ".git"))
}
func repoRoot(ctx context.Context, path string) string {
	root, _ := git(ctx, path, "rev-parse", "--show-toplevel")
	return root
}
func discover(ctx context.Context, want string, roots []string) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, candidate := range []string{root, filepath.Join(root, filepath.Base(want)), filepath.Join(filepath.Dir(root), filepath.Base(want))} {
			origin, err := git(ctx, candidate, "remote", "get-url", "origin")
			if err == nil && identity(origin) == identity(want) {
				return repoRoot(ctx, candidate)
			}
		}
	}
	return ""
}

func (r *runner) prepareSources(ctx context.Context, watch []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	workingRepo := repoRoot(ctx, cwd)
	roots := []string{workingRepo, cwd, repoRoot(ctx, filepath.Dir(r.o.file))}
	explicitPR := false
	for _, alias := range slices.Sorted(maps.Keys(r.o.sources)) {
		dir := r.o.sources[alias]
		if len(watch) == 0 || slices.Contains(watch, alias) {
			explicitPR = true
		}
		if _, ok := r.doc.Spec.Sources[alias]; !ok {
			return fmt.Errorf("unknown --source alias %s", alias)
		}
		if root := repoRoot(ctx, dir); root != "" {
			roots = append(roots, root)
		}
	}
	remotePR, err := r.clonePRAlias(watch, explicitPR)
	if err != nil {
		return err
	}
	r.sources = map[string]checkout{}
	pinnedPR := false
	for _, alias := range slices.Sorted(maps.Keys(r.doc.Spec.Sources)) {
		spec := r.doc.Spec.Sources[alias]
		ref := wf.SourceRevisionRef(spec)
		dir, explicit := r.o.sources[alias]
		lookupRef := ref
		cloned := r.o.cloneProtocol != "" && !explicit
		if cloned {
			if alias == remotePR {
				ref = fmt.Sprintf("refs/pull/%d/head", r.o.pr)
				pinnedPR = true
			}
			dir, err = r.cloneSource(ctx, alias, spec.Repository, ref)
			if err != nil {
				return fmt.Errorf("source %s: %w", alias, err)
			}
			lookupRef = "HEAD"
		} else {
			if explicit {
				dir = repoRoot(ctx, dir)
			} else {
				dir = discover(ctx, spec.Repository, roots)
			}
			if dir == "" {
				return fmt.Errorf("missing local checkout for %s (%s); supply --source %s=/path/to/checkout or use --clone https", alias, spec.Repository, alias)
			}
			isWatched := len(watch) == 0 || slices.Contains(watch, alias)
			if r.o.pr > 0 && isWatched && (r.o.prSource == "" || r.o.prSource == alias) && (explicit || !explicitPR && dir == workingRepo) {
				ref = "HEAD"
				lookupRef = ref
				pinnedPR = true
			}
		}
		sha, err := git(ctx, dir, "rev-parse", "--verify", "--end-of-options", lookupRef+"^{commit}")
		if err != nil && lookupRef != "HEAD" {
			sha, err = git(ctx, dir, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+ref+"^{commit}")
		}
		if err != nil {
			return fmt.Errorf("source %s: ref %s is unavailable locally: %w", alias, ref, err)
		}
		branch := ref
		if ref == "HEAD" && !cloned {
			if name, e := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); e == nil {
				branch = name
			}
		}
		c := checkout{repo: dir, revision: core.WorkflowSourceRevision{Alias: alias, Repository: spec.Repository, Branch: branch, CommitSHA: sha, Path: spec.Path}}
		c.dir, err = materialize(ctx, r.root, alias, c)
		if err != nil {
			return err
		}
		r.sources[alias] = c
		fmt.Fprintf(r.out, "Source %s: %s (%s)\n", alias, sha, branch)
	}
	if r.o.pr > 0 && !pinnedPR {
		return fmt.Errorf("supply --source for the PR checkout (%s); a PR number alone cannot identify its commit offline", strings.Join(watch, ", "))
	}
	return nil
}
func materialize(ctx context.Context, root, alias string, c checkout) (string, error) {
	dst := filepath.Join(root, alias)
	if _, err := git(ctx, root, "clone", "--quiet", "--shared", "--no-checkout", "--", c.repo, dst); err != nil {
		return "", fmt.Errorf("checkout %s: %w", alias, err)
	}
	if _, err := git(ctx, dst, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", c.revision.CommitSHA); err != nil {
		return "", fmt.Errorf("checkout %s: %w", alias, err)
	}
	dir, err := inside(dst, c.revision.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s: source path is not a directory", alias)
	}
	return dir, nil
}
func (r *runner) revisions() map[string]core.WorkflowSourceRevision {
	revs := map[string]core.WorkflowSourceRevision{}
	for k, c := range r.sources {
		revs[k] = c.revision
	}
	return revs
}
