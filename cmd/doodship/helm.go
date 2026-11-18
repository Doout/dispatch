package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	wf "github.com/doout/dispatch/internal/workflowfile"
	"sigs.k8s.io/yaml"
)

type release struct {
	name, namespace, kube, chart, url, owner string
	exists                                   bool
	files                                    []string
	values                                   map[string]any
	bindings                                 map[string]wf.OutputBinding
}

var releaseName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func (r *runner) prepareReleases() ([]release, error) {
	var releases []release
	targets := map[string]bool{}
	seen := map[[3]string]bool{}
	for _, stage := range r.doc.Spec.Stages {
		targets[stage.TargetRef] = true
		kube := stage.TargetRef
		if override, ok := r.o.targets[kube]; ok {
			kube = override
		}
		for _, key := range stage.Deploy {
			h := r.doc.Spec.Deployments[key].Helm
			rel := release{kube: kube, bindings: h.Bindings, url: stage.URL}
			var err error
			rel.name, err = wf.RenderRuntime(h.ReleaseName, nil, r.revisions(), nil, &stage)
			if err != nil {
				return nil, err
			}
			if rel.name == "" {
				rel.name = key
			}
			if !releaseName.MatchString(rel.name) || len(rel.name) > 53 {
				return nil, fmt.Errorf("invalid Helm release name %q", rel.name)
			}
			rel.namespace, err = wf.RenderRuntime(h.Namespace, nil, r.revisions(), nil, &stage)
			if err != nil {
				return nil, err
			}
			if rel.namespace == "" {
				rel.namespace = "default"
			}
			identity := [3]string{kube, rel.namespace, rel.name}
			if seen[identity] {
				return nil, fmt.Errorf("duplicate release %s in %s/%s", rel.name, kube, rel.namespace)
			}
			seen[identity] = true
			if r.o.pr > 0 {
				hash := sha256.New()
				fmt.Fprintf(hash, "%s\n%d\n", r.doc.Metadata.Name, r.o.pr)
				for _, alias := range slices.Sorted(maps.Keys(r.sources)) {
					fmt.Fprintf(hash, "%s=%s\n", alias, r.sources[alias].revision.Repository)
				}
				rel.owner = fmt.Sprintf("%x", hash.Sum(nil)[:12])
			}
			rel.chart, err = inside(r.sources[h.SourceRef].dir, h.ChartPath)
			if err != nil {
				return nil, fmt.Errorf("chart %s: %w", key, err)
			}
			if _, err = inside(rel.chart, "Chart.yaml"); err != nil {
				return nil, fmt.Errorf("chart %s: %w", key, err)
			}
			for i, file := range h.ValuesFiles {
				path, err := inside(r.sources[file.SourceRef].dir, file.Path)
				if err != nil {
					return nil, fmt.Errorf("values %s:%s: %w", file.SourceRef, file.Path, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				var values map[string]any
				if err = yaml.Unmarshal(raw, &values, func(dec *json.Decoder) *json.Decoder {
					dec.UseNumber()
					return dec
				}); err != nil {
					return nil, fmt.Errorf("invalid Helm values file %s: %w", file.Path, err)
				}
				// _pipeline contains Dispatch configuration and is removed by the hosted runner too.
				if _, ok := values["_pipeline"]; ok {
					delete(values, "_pipeline")
					path = filepath.Join(r.root, fmt.Sprintf("values-%d-%d.json", len(releases), i))
					if err = writeValues(path, values); err != nil {
						return nil, err
					}
				}
				rel.files = append(rel.files, path)
			}
			rel.values, err = wf.RenderValues(h.Values, r.revisions(), stage)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(r.out, "Stage %s: context=%s namespace=%s release=%s\n", stage.Name, kube, rel.namespace, rel.name)
			releases = append(releases, rel)
		}
	}
	for k := range r.o.targets {
		if !targets[k] {
			return nil, fmt.Errorf("unknown target %s", k)
		}
	}
	return releases, nil
}
func helm(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "helm", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("helm %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(data)), nil
}
func checkReleases(ctx context.Context, releases []release) error {
	if len(releases) == 0 {
		return nil
	}
	version, err := helm(ctx, "version", "--template", "{{.Version}}")
	if err != nil {
		return err
	}
	for i := range releases {
		r := &releases[i]
		args := []string{"list", "--kube-context", r.kube, "--namespace", r.namespace, "--short", "--filter", "^" + regexp.QuoteMeta(r.name) + "$"}
		// Helm 4 lists all release statuses by default and removed --all.
		if strings.HasPrefix(version, "v3.") {
			args = append(args, "--all")
		}
		found, err := helm(ctx, args...)
		if err != nil {
			return fmt.Errorf("check target %s: %w", r.kube, err)
		}
		r.exists = found == r.name
		if r.exists && r.owner != "" {
			owned, err := helm(ctx, append(args, "--selector", "doodship-owner="+r.owner)...)
			if err != nil {
				return err
			}
			if owned != r.name {
				return fmt.Errorf("preview release %s is already in use; choose a distinct --instance ID", r.name)
			}
		}
	}
	return nil
}
func writeValues(path string, v map[string]any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
func (r *runner) deploy(ctx context.Context, rel release, outputs map[string]map[string]string) error {
	for _, path := range slices.Sorted(maps.Keys(rel.bindings)) {
		ref := rel.bindings[path].OutputRef
		job, name, _ := strings.Cut(ref, ".")
		value, ok := outputs[job][name]
		if !ok {
			return fmt.Errorf("missing job output %s", ref)
		}
		if err := wf.SetNestedValue(rel.values, path, value); err != nil {
			return err
		}
	}
	path := filepath.Join(r.root, "overrides.json")
	if err := writeValues(path, rel.values); err != nil {
		return err
	}
	op := "install"
	if rel.exists {
		op = "upgrade"
	}
	args := []string{op, rel.name, rel.chart, "--kube-context", rel.kube, "--namespace", rel.namespace, "--wait"}
	if op == "upgrade" {
		args = append(args, "--reset-values")
	}
	for _, file := range rel.files {
		args = append(args, "-f", file)
	}
	args = append(args, "-f", path)
	if rel.owner != "" {
		args = append(args, "--labels", "doodship-owner="+rel.owner)
	}
	fmt.Fprintf(r.out, "Helm %s %s\n", op, rel.name)
	if _, err := helm(ctx, args...); err != nil {
		return err
	}
	fmt.Fprintln(r.out, strings.TrimSpace("Deployed "+rel.name+" "+rel.url))
	return nil
}
