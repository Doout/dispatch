package workflow

import (
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

const templateFixture = `apiVersion: dispatch/v1alpha1
kind: ApplicationTemplate
metadata:
  name: slots
spec:
  files:
    path: values/slots
    pattern: slot*.yaml
  parameters:
    chartRef: baseline
    target: dev
  template:
    apiVersion: dispatch/v1alpha1
    kind: Application
    metadata:
      name: ${slot.name}
    spec:
      sources:
        app:
          repository: owner/app
          ref: ${param.chartRef}
        config:
          repository: owner/config
          ref: ${config.revision}
      jobs:
        build:
          runFrom: app
          run: echo "{{ sources.app.commit }}"
      deployments:
        ${slot.name}:
          helm:
            sourceRef: app
            releaseName: ${slot.name}
            valuesFiles:
              - sourceRef: config
                path: ${slot.path}
      stages:
        - name: development
          targetRef: ${param.target}
          deploy: ['${slot.name}']
`

func TestExpandTemplateIndependentApplications(t *testing.T) {
	config := []githubapp.RepositoryFile{{Path: "deployment/agentops-slots.template.yaml", Contents: []byte(strings.Replace(templateFixture, "path: values/slots", "path: ./values/slots", 1))}}
	input := []githubapp.RepositoryFile{
		{Path: "values/slots/slot2.yaml", Contents: []byte("_pipeline:\n  chartRef: experiment\n  target: staging\nreplicas: 2")},
		{Path: "values/slots/slot1.yaml", Contents: []byte("replicas: 1")},
		{Path: "values/slots/connections.example.yaml", Contents: []byte("not: an application")},
	}
	calls := 0
	got, err := expandConfiguration(config, strings.Repeat("a", 40), func(dir string) ([]githubapp.RepositoryFile, error) {
		calls++
		if dir != "values/slots" {
			t.Fatal(dir)
		}
		return input, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || calls != 1 {
		t.Fatal(got, calls)
	}
	for i, name := range []string{"slot1", "slot2"} {
		d := got[i].document
		if d.Kind != KindApplication || d.Metadata.Name != name || d.Template != nil {
			t.Fatal(d)
		}
		if d.Spec.Sources["config"].Ref != strings.Repeat("a", 40) {
			t.Fatal("discovery revision not pinned")
		}
		if d.Spec.Deployments[name].Helm.ValuesFiles[0].Path != "values/slots/"+name+".yaml" {
			t.Fatal(d)
		}
		if d.Spec.Jobs["build"].Run != `echo "{{ sources.app.commit }}"` {
			t.Fatal("runtime expression changed")
		}
	}
	if got[0].document.Spec.Sources["app"].Ref != "baseline" || got[1].document.Spec.Sources["app"].Ref != "experiment" {
		t.Fatal("parameter override leaked between slots")
	}
	if got[1].document.Spec.Stages[0].TargetRef != "staging" {
		t.Fatal("target override lost")
	}
}
func TestTemplateValidationAndEmptyDiscovery(t *testing.T) {
	fixture := templateFixture
	for _, test := range []struct{ name, contents string }{
		{"unknown parameter", "_pipeline: {typo: bad}"},
		{"non-string parameter", "_pipeline: {target: 12}"},
		{"multiple documents", "replicas: 1\n---\nreplicas: 2"},
		{"duplicate key", "replicas: 1\nreplicas: 2"},
		{"invalid metadata", "_pipeline: false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := expandConfiguration([]githubapp.RepositoryFile{{Path: "deployment/template.yaml", Contents: []byte(fixture)}}, "main", func(string) ([]githubapp.RepositoryFile, error) {
				return []githubapp.RepositoryFile{{Path: "values/slots/slot1.yaml", Contents: []byte(test.contents)}}, nil
			})
			if err == nil {
				t.Fatal("invalid slot accepted")
			}
		})
	}
	got, err := expandConfiguration([]githubapp.RepositoryFile{{Path: "deployment/template.yaml", Contents: []byte(fixture)}}, "main", func(string) ([]githubapp.RepositoryFile, error) { return nil, nil })
	if err != nil || len(got) != 0 {
		t.Fatal("last slot removal failed", got, err)
	}
	for _, invalid := range []string{strings.Replace(fixture, "path: values/slots", "path: ../outside", 1), strings.Replace(fixture, "pattern: slot*.yaml", "pattern: '**/*.yaml'", 1)} {
		if _, err := Parse("template.yaml", []byte(invalid)); err == nil {
			t.Fatal("unsafe discovery accepted")
		}
	}
}
func TestTemplateScalarExpansionDoesNotInjectYAML(t *testing.T) {
	documents, err := Parse("template.yaml", []byte(templateFixture))
	if err != nil {
		t.Fatal(err)
	}
	documents[0].Template.Template.Spec.Jobs["build"] = JobSpec{RunFrom: "app", Run: "echo '${param.target}'"}
	got, err := expandApplication(documents[0], githubapp.RepositoryFile{Path: "values/slots/slot1.yaml", Contents: []byte("_pipeline:\n  target: |\n    dev\n    injected: true\n")}, "main")
	// The target may be rejected by ordinary Application validation; it must not
	// create a new YAML field or alter the job structure.
	if err == nil && got.Spec.Jobs["build"].Run != "echo 'dev\ninjected: true\n'" {
		t.Fatal(got)
	}
}
func TestPreserveIdentityAcrossTemplateMigration(t *testing.T) {
	old := core.WorkflowResource{ID: "stable", Name: "slot1", Kind: KindApplication, Path: "deployment/slot1.yaml", Active: false, State: "paused"}
	got, found, err := existingApplication([]core.WorkflowResource{old}, "deployment/agentops-slots.template.yaml", KindApplication, "slot1")
	if err != nil || !found || got.ID != "stable" || got.Active || got.State != "paused" {
		t.Fatal(got, found, err)
	}
}
func TestTemplateDuplicateNamesRejected(t *testing.T) {
	fixture := templateFixture
	files := []githubapp.RepositoryFile{{Path: "deployment/one.yaml", Contents: []byte(fixture)}, {Path: "deployment/two.yaml", Contents: []byte(fixture)}}
	_, err := expandConfiguration(files, "main", func(string) ([]githubapp.RepositoryFile, error) {
		return []githubapp.RepositoryFile{{Path: "values/slots/slot1.yaml", Contents: []byte("replicas: 1")}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "both define") {
		t.Fatal(err)
	}
}
