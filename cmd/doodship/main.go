// doodship runs a trusted workflow from local Git checkouts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	wf "github.com/doout/dispatch/internal/workflowfile"
	"gopkg.in/yaml.v3"
)

const workflowUsage = "Usage: doodship workflow run --file FILE [--pr NUMBER] [--source alias=checkout] [--target ref=context]"

const updateUsage = "Usage: doodship update"
const usage = workflowUsage + "\n" + updateUsage

type options struct {
	file, envFile, envExample, instance string
	pr                                  int
	sources, targets, vars              map[string]string
	plan, listInputs, nonInteractive    bool
}

func mapping(m map[string]string) func(string) error {
	return func(arg string) error {
		k, v, ok := strings.Cut(arg, "=")
		if !ok || k == "" || v == "" {
			return errors.New("expected name=value")
		}
		if _, ok := m[k]; ok {
			return fmt.Errorf("%s specified twice", k)
		}
		m[k] = v
		return nil
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "doodship:", err)
		code := 1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			code = 130
		}
		os.Exit(code)
	}
}
func run(ctx context.Context, args []string, out, log io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, usage)
		return nil
	}
	if len(args) > 0 && args[0] == "update" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			fmt.Fprintln(out, updateUsage+"\nInstall the latest stable release over the running executable.")
			return nil
		}
		if len(args) != 1 {
			return errors.New(updateUsage)
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return updateExecutable(ctx, executable, out, log)
	}
	if len(args) < 2 || args[0] != "workflow" || args[1] != "run" {
		return errors.New(usage)
	}
	o := options{sources: map[string]string{}, targets: map[string]string{}, vars: map[string]string{}}
	flags := flag.NewFlagSet("workflow run", flag.ContinueOnError)
	flags.SetOutput(log)
	flags.Usage = func() { fmt.Fprintln(log, workflowUsage); flags.PrintDefaults() }
	flags.StringVar(&o.file, "file", "", "Workflow YAML file")
	flags.IntVar(&o.pr, "pr", 0, "PR number for a WorkflowTemplate")
	flags.StringVar(&o.instance, "instance", "", "Preview instance ID (defaults to the PR number)")
	flags.Func("source", "alias=local Git checkout (repeatable)", mapping(o.sources))
	flags.Func("target", "targetRef=kube-context (defaults to targetRef)", mapping(o.targets))
	flags.Func("var", "name=value for a declared nonsecret binding", mapping(o.vars))
	flags.StringVar(&o.envFile, "env-file", "", "Local file of literal NAME=value bindings")
	flags.BoolVar(&o.plan, "plan", false, "Print pinned sources, required bindings and releases without executing")
	flags.BoolVar(&o.plan, "dry-run", false, "Alias for --plan; inspect without running jobs or Helm")
	flags.BoolVar(&o.listInputs, "list-inputs", false, "Show binding availability and job environment names without preparing checkouts")
	flags.StringVar(&o.envExample, "write-env-example", "", "With --list-inputs, create a new env template at this path (e.g. .env.example)")
	flags.BoolVar(&o.nonInteractive, "non-interactive", false, "Fail if required bindings are missing")
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.file == "" || flags.NArg() != 0 {
		return errors.New(workflowUsage)
	}
	if o.envExample != "" && !o.listInputs {
		return errors.New("--write-env-example requires --list-inputs")
	}
	doc, watch, err := load(o)
	if err != nil {
		return err
	}
	r := runner{o: o, doc: doc, out: &lockedWriter{w: out}}
	if o.listInputs && !o.plan {
		_, err := r.bindings(ctx)
		return err
	}
	root, err := os.MkdirTemp("", "doodship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	r.root = root
	if err := r.prepareSources(ctx, watch); err != nil {
		return err
	}
	releases, err := r.prepareReleases()
	if err != nil {
		return err
	}
	bindings, err := r.bindings(ctx)
	if err != nil {
		return err
	}
	if o.plan {
		return nil
	}
	if err := checkReleases(ctx, releases); err != nil {
		return err
	}
	outputs, err := r.jobs(ctx, bindings)
	if err != nil {
		return err
	}
	for _, rel := range releases {
		if err := r.deploy(ctx, rel, outputs); err != nil {
			return err
		}
	}
	return nil
}

type runner struct {
	o             options
	doc           wf.ApplicationDocument
	root          string
	out           io.Writer
	sources       map[string]checkout
	sharedJobDirs map[string]string
	sharedJobRoot string
}

func load(o options) (wf.ApplicationDocument, []string, error) {
	var doc wf.ApplicationDocument
	raw, err := os.ReadFile(o.file)
	if err != nil {
		return doc, nil, err
	}
	var meta wf.TypeMeta
	if err = yaml.Unmarshal(raw, &meta); err != nil {
		return doc, nil, err
	}
	var watch []string
	switch meta.Kind {
	case "WorkflowTemplate":
		if o.pr <= 0 {
			return doc, nil, errors.New("WorkflowTemplate requires --pr NUMBER")
		}
		id := o.instance
		if id == "" {
			id = strconv.Itoa(o.pr)
		}
		if !aliasName.MatchString(id) {
			return doc, nil, errors.New("invalid --instance ID")
		}
		rendered, err := wf.RenderTemplateText(string(raw), wf.TemplateVariables{ID: id, PRNumber: o.pr})
		if err != nil {
			return doc, nil, err
		}
		raw = []byte(rendered)
	case "Application":
		if o.pr != 0 || o.instance != "" {
			return doc, nil, errors.New("--pr and --instance apply only to WorkflowTemplate")
		}
	default:
		return doc, nil, fmt.Errorf("unsupported workflow kind %q", meta.Kind)
	}
	// Decode one document strictly so an unsupported stage or trigger cannot silently run.
	var input struct {
		wf.TypeMeta `yaml:",inline"`
		Metadata    wf.Metadata `yaml:"metadata"`
		Spec        struct {
			wf.ApplicationSpec `yaml:",inline"`
			Triggers           *struct {
				Comment struct {
					Sources []string `yaml:"sources"`
					Command string   `yaml:"command"`
				} `yaml:"pullRequestComment"`
			} `yaml:"triggers"`
		} `yaml:"spec"`
	}
	d := yaml.NewDecoder(strings.NewReader(string(raw)))
	d.KnownFields(true)
	if err = d.Decode(&input); err != nil {
		return doc, nil, err
	}
	if err = d.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return doc, nil, errors.New("expected one workflow per file")
	}
	doc = wf.ApplicationDocument{TypeMeta: input.TypeMeta, Metadata: input.Metadata, Spec: input.Spec.ApplicationSpec}
	if trig := input.Spec.Triggers; trig != nil {
		if meta.Kind != "WorkflowTemplate" || len(trig.Comment.Sources) == 0 {
			return doc, nil, errors.New("invalid pullRequestComment trigger")
		}
		watch = trig.Comment.Sources
		for _, alias := range watch {
			if _, ok := doc.Spec.Sources[alias]; !ok {
				return doc, nil, fmt.Errorf("unknown trigger source %s", alias)
			}
		}
	}
	if len(doc.Spec.PreviewServices) > 0 {
		return doc, nil, errors.New("preview services require a Dispatch controller")
	}
	for _, jobs := range []map[string]wf.JobSpec{doc.Spec.Jobs, doc.Spec.Finally} {
		for name, job := range jobs {
			if job.WorkerMode != "" {
				return doc, nil, fmt.Errorf("job %s requires a %s worker", name, job.WorkerMode)
			}
			for env, b := range job.Secrets {
				if b.ServiceRef != "" {
					return doc, nil, fmt.Errorf("job %s: %s requires a controller service binding", name, env)
				}
			}
		}
	}
	wf.ApplyApplicationDefaults(&doc.Spec)
	err = wf.ValidateApplication(doc, func(dep wf.DeploymentSpec) error {
		if len(dep.ServiceBindings) > 0 {
			return errors.New("controller service bindings cannot run locally")
		}
		return nil
	})
	if err != nil {
		return doc, nil, err
	}
	for _, stage := range doc.Spec.Stages {
		if stage.Approval != "automatic" || len(stage.Checks) > 0 || len(stage.ServiceBindings) > 0 {
			return doc, nil, fmt.Errorf("stage %s: local runs support automatic Helm stages without controller approvals, checks or service bindings", stage.Name)
		}
	}
	return doc, watch, nil
}

func inside(root, rel string) (string, error) {
	path, err := wf.SafeJoin(root, rel)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("path %q escapes checkout", rel)
	}
	return resolved, nil
}

// Parallel jobs share a terminal or a caller-provided writer.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}
