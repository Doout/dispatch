package workflow

import (
	"fmt"
	"sort"
	"strings"
)

type Topology struct {
	Columns []TopologyColumn `json:"columns"`
	Nodes   []TopologyNode   `json:"nodes"`
	Edges   []TopologyEdge   `json:"edges"`
}

type TopologyColumn struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type TopologyNode struct {
	ID       string            `json:"id"`
	Column   string            `json:"column"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Detail   string            `json:"detail,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type TopologyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

func BuildTopology(path string, contents []byte) (Topology, error) {
	documents, err := Parse(path, contents)
	if err != nil {
		return Topology{}, err
	}
	if len(documents) != 1 {
		return Topology{}, fmt.Errorf("%s must contain one resource", path)
	}
	document := documents[0]
	if document.Spec != nil {
		return applicationTopology(*document.Spec), nil
	}
	if document.Pipeline != nil {
		return pipelineTopology(*document.Pipeline), nil
	}
	return Topology{}, fmt.Errorf("%s has no workflow specification", path)
}

func applicationTopology(spec ApplicationSpec) Topology {
	topology := Topology{Columns: []TopologyColumn{{ID: "sources", Label: "Sources"}}}
	if len(spec.Jobs)+len(spec.Finally) > 0 {
		topology.Columns = append(topology.Columns, TopologyColumn{ID: "jobs", Label: "Jobs"})
	}
	if len(spec.Deployments) > 0 {
		topology.Columns = append(topology.Columns, TopologyColumn{ID: "deployments", Label: "Deployments"})
	}
	for index, stage := range spec.Stages {
		topology.Columns = append(topology.Columns, TopologyColumn{ID: fmt.Sprintf("stage-%d", index), Label: stage.Name})
	}
	addSources(&topology, spec.Sources)
	addJobs(&topology, spec.Jobs, "job", "Job")
	addJobs(&topology, spec.Finally, "finally", "Final job")

	for _, name := range sortedKeys(spec.Deployments) {
		deployment := spec.Deployments[name]
		id := nodeID("deployment", name)
		detail := deployment.Helm.ReleaseName
		if detail == "" {
			detail = "Helm release"
		}
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id, Column: "deployments", Kind: "deployment", Label: name, Detail: detail, Metadata: compactMetadata(map[string]string{
			"Chart source": deployment.Helm.SourceRef,
			"Chart path":   deployment.Helm.ChartPath,
			"Namespace":    deployment.Helm.Namespace,
		})})
		if _, ok := spec.Sources[deployment.Helm.SourceRef]; ok {
			topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("source", deployment.Helm.SourceRef), To: id, Kind: "chart"})
		}
		for _, file := range deployment.Helm.ValuesFiles {
			topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("source", file.SourceRef), To: id, Kind: "values"})
		}
		for _, binding := range deployment.Helm.Bindings {
			job := strings.SplitN(binding.OutputRef, ".", 2)[0]
			if _, ok := spec.Jobs[job]; ok {
				topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("job", job), To: id, Kind: "output"})
			}
		}
	}

	for index, stage := range spec.Stages {
		id := nodeID("stage", stage.Name)
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id, Column: fmt.Sprintf("stage-%d", index), Kind: "stage", Label: stage.Name, Detail: stage.TargetRef, Metadata: compactMetadata(map[string]string{
			"Approval": stage.Approval,
			"URL":      stage.URL,
		})})
		for _, deployment := range stage.Deploy {
			if _, ok := spec.Deployments[deployment]; ok {
				topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("deployment", deployment), To: id, Kind: "promotes"})
			}
		}
		if index > 0 {
			topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("stage", spec.Stages[index-1].Name), To: id, Kind: "promotes"})
		}
	}
	return topology
}

func pipelineTopology(spec PipelineSpec) Topology {
	topology := Topology{Columns: []TopologyColumn{
		{ID: "sources", Label: "Sources"},
		{ID: "jobs", Label: "Jobs"},
		{ID: "final", Label: "Final jobs"},
	}}
	addSources(&topology, spec.Sources)
	addJobs(&topology, spec.Jobs, "job", "Job")
	addJobs(&topology, spec.Finally, "finally", "Final job")
	for _, finalName := range sortedKeys(spec.Finally) {
		for _, jobName := range sortedKeys(spec.Jobs) {
			topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("job", jobName), To: nodeID("finally", finalName), Kind: "completes"})
		}
	}
	return topology
}

func addSources(topology *Topology, sources map[string]SourceSpec) {
	for _, name := range sortedKeys(sources) {
		source := sources[name]
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: nodeID("source", name), Column: "sources", Kind: "source", Label: name, Detail: repositoryName(source.Repository), Metadata: compactMetadata(map[string]string{
			"Branch": sourceRevisionRef(source),
			"Path":   source.Path,
		})})
	}
}

func addJobs(topology *Topology, jobs map[string]JobSpec, prefix, kind string) {
	for _, name := range sortedKeys(jobs) {
		job := jobs[name]
		id := nodeID(prefix, name)
		column := "jobs"
		if prefix == "finally" && hasColumn(*topology, "final") {
			column = "final"
		}
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id, Column: column, Kind: prefix, Label: name, Detail: kind, Metadata: compactMetadata(map[string]string{
			"Runs from": job.RunFrom,
			"Outputs":   strings.Join(job.Outputs, ", "),
		})})
		seen := map[string]bool{}
		for _, source := range append([]string{job.RunFrom}, job.Sources...) {
			if source == "" || seen[source] {
				continue
			}
			seen[source] = true
			topology.Edges = append(topology.Edges, TopologyEdge{From: nodeID("source", source), To: id, Kind: "input"})
		}
	}
}

func nodeID(kind, name string) string { return kind + ":" + name }

func hasColumn(topology Topology, id string) bool {
	for _, column := range topology.Columns {
		if column.ID == id {
			return true
		}
	}
	return false
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compactMetadata(values map[string]string) map[string]string {
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			delete(values, key)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func repositoryName(repository string) string {
	value := strings.TrimSuffix(repository, ".git")
	value = strings.TrimRight(value, "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	if index := strings.LastIndex(value, ":"); index >= 0 {
		return value[index+1:]
	}
	return value
}
