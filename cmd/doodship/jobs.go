package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/core"
	wf "github.com/doout/dispatch/internal/workflowfile"
	"golang.org/x/term"
)

type bindingUse struct{ job, env, key string }

func (r *runner) bindings(ctx context.Context) (map[string]string, error) {
	uses := map[string][]bindingUse{}
	for _, group := range []struct {
		prefix string
		jobs   map[string]wf.JobSpec
	}{{"", r.doc.Spec.Jobs}, {"finally/", r.doc.Spec.Finally}} {
		for _, job := range slices.Sorted(maps.Keys(group.jobs)) {
			secrets := group.jobs[job].Secrets
			for _, env := range slices.Sorted(maps.Keys(secrets)) {
				b := secrets[env]
				uses[b.SecretRef] = append(uses[b.SecretRef], bindingUse{group.prefix + job, env, b.Key})
			}
		}
	}
	inspect := r.o.plan || r.o.listInputs
	values := map[string]string{}
	if r.o.envFile != "" {
		f, err := os.Open(r.o.envFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for line := 1; s.Scan(); line++ {
			text := s.Text()
			if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
				continue
			}
			key, val, ok := strings.Cut(text, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return nil, fmt.Errorf("invalid NAME=value on env-file line %d", line)
			}
			values[strings.TrimSpace(key)] = val
		}
		if err = s.Err(); err != nil {
			return nil, err
		}
	}
	for k, v := range r.o.vars {
		if _, ok := uses[k]; !ok {
			return nil, fmt.Errorf("unknown binding %s", k)
		}
		values[k] = v
	}
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(uses)) {
		source := "env file"
		if _, ok := r.o.vars[name]; ok {
			source = "--var"
		}
		if values[name] == "" {
			values[name] = os.Getenv(name)
			source = "environment"
		}
		if inspect {
			status := "set (" + source + ")"
			if values[name] == "" {
				status = "missing"
			}
			fmt.Fprintf(r.out, "Required binding: %s [%s]\n", name, status)
			for _, u := range uses[name] {
				detail := ""
				if u.key != "" {
					detail = " (JSON key " + u.key + ")"
					if values[name] != "" {
						if _, err := wf.SelectJSONKey([]byte(values[name]), u.key); err != nil {
							detail += " [unavailable]"
						}
					}
				}
				fmt.Fprintf(r.out, "  %s -> %s%s\n", u.job, u.env, detail)
			}
			continue
		}
		if values[name] != "" {
			continue
		}
		if r.o.nonInteractive || !term.IsTerminal(int(os.Stdin.Fd())) {
			missing = append(missing, name)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s (hidden): ", name)
		b, err := readHidden(ctx, int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			missing = append(missing, name)
		}
		values[name] = string(b)
		clear(b)
	}
	if inspect && len(uses) == 0 {
		fmt.Fprintln(r.out, "No declared bindings.")
	}
	if r.o.listInputs {
		r.printRuntimeEnvironment()
		if r.o.envExample != "" {
			if err := writeEnvExample(r.o.envExample, uses); err != nil {
				return nil, err
			}
			fmt.Fprintln(r.out, "Created", r.o.envExample)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing bindings: %s (set environment variables or use --env-file)", strings.Join(missing, ", "))
	}
	// Validate all key selections before any job with side effects starts.
	if !inspect {
		for _, jobs := range []map[string]wf.JobSpec{r.doc.Spec.Jobs, r.doc.Spec.Finally} {
			for _, j := range jobs {
				if _, err := jobBindings(j, values); err != nil {
					return nil, err
				}
			}
		}
	}
	return values, nil
}

// Generate from declarations alone. Resolved values must never enter this file.
func writeEnvExample(path string, uses map[string][]bindingUse) error {
	var template strings.Builder
	template.WriteString("# Copy to a private env file and fill in the values you need.\n")
	for _, name := range slices.Sorted(maps.Keys(uses)) {
		if strings.ContainsAny(name, "=\r\n\x00") || strings.TrimSpace(name) != name || strings.HasPrefix(name, "#") {
			return fmt.Errorf("binding %q cannot be represented in an env file", name)
		}
		template.WriteByte('\n')
		for _, u := range uses[name] {
			fmt.Fprintf(&template, "# %s -> %s", u.job, u.env)
			if u.key != "" {
				fmt.Fprintf(&template, " (JSON key %s)", u.key)
			}
			template.WriteByte('\n')
		}
		fmt.Fprintf(&template, "%s=\n", name)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create env example: %w", err)
	}
	_, writeErr := f.WriteString(template.String())
	if err := errors.Join(writeErr, f.Close()); err != nil {
		os.Remove(path)
		return fmt.Errorf("write env example: %w", err)
	}
	return nil
}

func jobBindings(j wf.JobSpec, values map[string]string) (map[string]string, error) {
	result := map[string]string{}
	for name, b := range j.Secrets {
		val := values[b.SecretRef]
		if b.Key != "" {
			var err error
			val, err = wf.SelectJSONKey([]byte(val), b.Key)
			if err != nil {
				return nil, fmt.Errorf("binding %s: %w", name, err)
			}
		}
		result[name] = val
	}
	return result, nil
}

func (r *runner) jobs(ctx context.Context, bindings map[string]string) (map[string]map[string]string, error) {
	limit := r.doc.Spec.MaxParallelJobs
	if limit == 1 {
		var err error
		r.sharedJobRoot, err = os.MkdirTemp(r.root, "serial-")
		if err != nil {
			return nil, err
		}
		r.sharedJobDirs = map[string]string{}
	}
	if limit == 0 {
		limit = 2
	}
	runJob := func(ctx context.Context, name string, j wf.JobSpec) (map[string]string, error) {
		return r.job(ctx, name, j, bindings)
	}
	outputs, runErr := wf.RunJobs(ctx, r.doc.Spec.Jobs, limit, runJob)
	for _, name := range slices.Sorted(maps.Keys(r.doc.Spec.Finally)) {
		_, err := r.job(ctx, "finally-"+name, r.doc.Spec.Finally[name], bindings)
		runErr = errors.Join(runErr, err)
	}
	return outputs, runErr
}

// Mask complete lines before streaming them. Discard an oversized line without
// printing a partial secret at the truncation boundary.
type jobLog struct {
	out      io.Writer
	masks    []string
	line     bytes.Buffer
	overflow bool
}

func newJobLog(out io.Writer, bindings map[string]string) *jobLog {
	l := &jobLog{out: out}
	for _, v := range bindings {
		for _, part := range strings.Split(v, "\n") {
			if part != "" {
				l.masks = append(l.masks, part)
			}
		}
	}
	slices.SortFunc(l.masks, func(a, b string) int { return len(b) - len(a) })
	return l
}

func (l *jobLog) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		end := bytes.IndexByte(b, '\n')
		size := len(b)
		if end >= 0 {
			size = end + 1
		}
		if l.line.Len()+size > 1<<20 {
			l.overflow = true
			l.line.Reset()
		}
		if !l.overflow {
			l.line.Write(b[:size])
		}
		b = b[size:]
		if end >= 0 {
			l.flush()
		}
	}
	return n, nil
}
func (l *jobLog) flush() {
	if l.overflow {
		fmt.Fprintln(l.out, "[oversized log line omitted]")
	} else if l.line.Len() > 0 {
		text := l.line.String()
		for _, mask := range l.masks {
			text = strings.ReplaceAll(text, mask, "[REDACTED]")
		}
		fmt.Fprint(l.out, text)
	}
	l.line.Reset()
	l.overflow = false
}

func (r *runner) job(ctx context.Context, name string, j wf.JobSpec, bindings map[string]string) (map[string]string, error) {
	fmt.Fprintln(r.out, "Running job", name)
	root, err := os.MkdirTemp(r.root, "job-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	paths := map[string]string{}
	revs := map[string]core.WorkflowSourceRevision{}
	for _, alias := range wf.JobSourceAliases(j) {
		c := r.sources[alias]
		dir := r.sharedJobDirs[alias]
		var err error
		if dir == "" {
			checkoutRoot := root
			if r.sharedJobDirs != nil {
				checkoutRoot = r.sharedJobRoot
			}
			dir, err = materialize(ctx, checkoutRoot, alias, c)
			if err == nil && r.sharedJobDirs != nil {
				r.sharedJobDirs[alias] = dir
			}
		}
		if err != nil {
			return nil, err
		}
		paths[alias] = dir
		revs[alias] = c.revision

	}
	script, err := wf.RenderRuntime(j.Run, paths, revs, nil, nil)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(root, "outputs-")
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	env := jobEnvironment(j, paths, revs, f.Name(), filepath.Base(r.root))
	selected, err := jobBindings(j, bindings)
	if err != nil {
		return nil, err
	}
	for key, value := range selected {
		env = append(env, key+"="+value)
	}
	logs := newJobLog(r.out, selected)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-eu", "-c", script)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = paths[j.RunFrom], env, logs, logs
	// Stop build clients spawned by the shell too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	logs.flush()
	if err != nil {
		return nil, err
	}
	outputs, err := wf.ReadJobOutputs(f.Name(), j.Outputs)
	if err != nil {
		return nil, err
	}
	return outputs, nil
}

// ReadPassword restores echo on normal return. Restore it on interruption as well.
func readHidden(ctx context.Context, fd int) ([]byte, error) {
	state, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}
	type result struct {
		value []byte
		err   error
	}
	done := make(chan result, 1)
	go func() { value, err := term.ReadPassword(fd); done <- result{value, err} }()
	select {
	case v := <-done:
		return v.value, v.err
	case <-ctx.Done():
		_ = term.Restore(fd, state)
		return nil, ctx.Err()
	}
}

func jobEnvironment(j wf.JobSpec, paths map[string]string, revs map[string]core.WorkflowSourceRevision, output, revision string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "SSH_AUTH_SOCK", "KUBECONFIG"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	for _, alias := range wf.JobSourceAliases(j) {
		prefix := "DISPATCH_SOURCE_" + strings.ToUpper(strings.ReplaceAll(alias, "-", "_"))
		env = append(env, prefix+"_PATH="+paths[alias], prefix+"_COMMIT="+revs[alias].CommitSHA, prefix+"_BRANCH="+revs[alias].Branch)
	}
	env = append(env, "DISPATCH_OUTPUT_FILE="+output, "GITHUB_OUTPUT="+output, "DISPATCH_REVISION_ID="+revision)
	return env
}

// Use the executor's environment builder so inspection includes only settings
// actually inherited on this laptop. Never write the values to the report.
func (r *runner) printRuntimeEnvironment() {
	for _, group := range []struct {
		prefix string
		jobs   map[string]wf.JobSpec
	}{{"", r.doc.Spec.Jobs}, {"finally/", r.doc.Spec.Finally}} {
		for _, name := range slices.Sorted(maps.Keys(group.jobs)) {
			var names []string
			for _, pair := range jobEnvironment(group.jobs[name], nil, nil, "", "") {
				key, _, _ := strings.Cut(pair, "=")
				names = append(names, key)
			}
			fmt.Fprintf(r.out, "Runtime environment for %s%s: %s\n", group.prefix, name, strings.Join(names, ", "))
		}
	}
}
