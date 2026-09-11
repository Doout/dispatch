package deploy

import (
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	helmrelease "helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

func TestReleaseValuesMatchDeployment(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	finished := started.Add(30 * time.Second)
	deployment := core.Deployment{CreatedAt: started, FinishedAt: &finished}
	installed := &helmrelease.Release{Info: &helmrelease.Info{LastDeployed: helmtime.Time{Time: started.Add(time.Second)}}, Config: map[string]any{"replicas": 1, "password": "sensitive"}}
	snapshot := map[string]any{"replicas": float64(1), "password": "••••••••"}
	if !releaseValuesMatch(installed, deployment, snapshot) {
		t.Fatal("equivalent serialized snapshot not matched")
	}
	installed.Info.LastDeployed = helmtime.Time{Time: finished.Add(time.Second)}
	if releaseValuesMatch(installed, deployment, snapshot) {
		t.Fatal("newer chart used for historical deployment")
	}
	installed.Info.LastDeployed = helmtime.Time{Time: started.Add(time.Second)}
	installed.Config["replicas"] = 2
	if releaseValuesMatch(installed, deployment, snapshot) {
		t.Fatal("different inputs matched")
	}
}

func TestMatchingReleaseUsesDeploymentHistory(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	finished := started.Add(time.Minute)
	deployment := core.Deployment{CreatedAt: started, FinishedAt: &finished}
	expected := map[string]any{"replicas": float64(1)}
	old := &helmrelease.Release{Version: 62, Info: &helmrelease.Info{LastDeployed: helmtime.Time{Time: started.Add(time.Second)}}, Config: map[string]any{"replicas": 1}}
	latest := &helmrelease.Release{Version: 64, Info: &helmrelease.Info{LastDeployed: helmtime.Time{Time: finished.Add(time.Minute)}}, Config: map[string]any{"replicas": 1}}
	wrongInputs := &helmrelease.Release{Version: 63, Info: old.Info, Config: map[string]any{"replicas": 2}}
	if got := matchingRelease([]*helmrelease.Release{latest, nil, wrongInputs, old}, deployment, expected); got != old {
		t.Fatal("did not select the exact historical revision")
	}
	if got := matchingRelease([]*helmrelease.Release{latest, wrongInputs}, deployment, expected); got != nil {
		t.Fatal("used another revision when deployment history was unavailable")
	}
}
