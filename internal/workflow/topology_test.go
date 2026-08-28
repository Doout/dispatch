package workflow

import "testing"

func TestBuildApplicationTopology(t *testing.T) {
	topology, err := BuildTopology("deployment/app.yaml", []byte(`apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: storefront
spec:
  sources:
    chart:
      repository: https://example.com/platform.git
    api:
      repository: https://example.com/api.git
  jobs:
    build-api:
      runFrom: api
      run: ./build.sh
      outputs: [image]
  deployments:
    storefront:
      helm:
        sourceRef: chart
        bindings:
          image:
            outputRef: build-api.image
  stages:
    - name: development
      targetRef: dev-cluster
      deploy: [storefront]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Columns) != 4 || len(topology.Nodes) != 5 || len(topology.Edges) != 4 {
		t.Fatalf("unexpected topology: %#v", topology)
	}
	assertEdge := func(from, to string) {
		t.Helper()
		for _, edge := range topology.Edges {
			if edge.From == from && edge.To == to {
				return
			}
		}
		t.Errorf("missing edge %s -> %s", from, to)
	}
	assertEdge("source:api", "job:build-api")
	assertEdge("source:chart", "deployment:storefront")
	assertEdge("job:build-api", "deployment:storefront")
	assertEdge("deployment:storefront", "stage:development")
}
