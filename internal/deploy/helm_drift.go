package deploy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

// HelmDriftRelease contains private rendered input from Helm's stored release,
// never a reconstruction from the live Kubernetes resources.
type HelmDriftRelease struct {
	Manifest string
	Matched  bool
	Revision int
}

func ReadHelmDriftRelease(ctx context.Context, server core.Server, namespace, name string, deployment core.Deployment) (HelmDriftRelease, error) {
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return HelmDriftRelease{}, errors.New("The deployment target credentials are unavailable.")
	}
	defer cleanup()
	settings := cli.New()
	settings.KubeConfig = prepared.Kubernetes.KubeconfigPath
	settings.KubeContext = prepared.Kubernetes.Context
	cfg := new(action.Configuration)
	getter := driftRESTGetter{RESTClientGetter: settings.RESTClientGetter(), ctx: ctx}
	if err = cfg.Init(getter, namespace, os.Getenv("HELM_DRIVER"), func(string, ...any) {}); err != nil {
		return HelmDriftRelease{}, errors.New("Cannot initialize the Helm release reader.")
	}
	history, err := action.NewHistory(cfg).Run(name)
	if err != nil {
		if ctx.Err() != nil {
			return HelmDriftRelease{}, errors.New("The check timed out while reading Helm release history.")
		}
		return HelmDriftRelease{}, errors.New("Helm release history could not be read. Check the release name, target access, and permission to read Helm storage.")
	}
	return selectDriftRelease(history, namespace, name, deployment)
}

func selectDriftRelease(history []*helmrelease.Release, namespace, name string, deployment core.Deployment) (HelmDriftRelease, error) {
	var latest, matched *helmrelease.Release
	matches := 0
	for _, candidate := range history {
		if candidate == nil || candidate.Info == nil || candidate.Name != name || candidate.Namespace != namespace {
			continue
		}
		if candidate.Info.Status != helmrelease.StatusDeployed && candidate.Info.Status != helmrelease.StatusSuperseded {
			continue
		}
		if latest == nil || candidate.Version > latest.Version {
			latest = candidate
		}
		// A bounded completed deployment and its immutable input snapshot must agree.
		if deployment.Snapshot.TargetID != "" && deployment.FinishedAt != nil && releaseValuesMatch(candidate, deployment, deployment.Snapshot.Values) {
			matched = candidate
			matches++
		}
	}
	if matches == 1 && matched.Manifest != "" {
		return HelmDriftRelease{Manifest: matched.Manifest, Matched: true, Revision: matched.Version}, nil
	}
	if latest != nil && latest.Manifest != "" {
		return HelmDriftRelease{Manifest: latest.Manifest, Revision: latest.Version}, nil
	}
	return HelmDriftRelease{}, errors.New("No retained successful Helm release is available to inspect.")
}

type driftRESTGetter struct {
	genericclioptions.RESTClientGetter
	ctx context.Context
}

func (g driftRESTGetter) ToRESTConfig() (*rest.Config, error) {
	cfg, err := g.RESTClientGetter.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = 10 * time.Second
	previous := cfg.WrapTransport
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		if previous != nil {
			next = previous(next)
		}
		return driftTransport{ctx: g.ctx, next: next}
	}
	return cfg, nil
}

type driftTransport struct {
	ctx  context.Context
	next http.RoundTripper
}

func (t driftTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(r.Clone(t.ctx))
}
