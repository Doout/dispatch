package remoteruntime

import (
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
)

// ValidateRoute binds agent evidence to accepted inputs. Public DNS and TLS
// readiness are verified independently by the controller, never by this receipt.
func (r Request) ValidateRoute(record *core.ApplicationRoute) error {
	if record == nil {
		return nil
	}
	plan, err := routing.Plan(r.Deployment, r.Application, r.Server)
	if err != nil || plan == nil || (r.Operation != runtimecontract.Deploy && r.Operation != runtimecontract.Rollback) {
		return errors.New("unexpected runtime route evidence")
	}
	if record.AppID != plan.AppID || record.ProjectID != plan.ProjectID || record.ServerID != plan.ServerID || record.Hostname != plan.Hostname || record.RequestedDeploymentID != plan.RequestedDeploymentID || record.EntryPoint != plan.EntryPoint || record.TLSResolver != plan.TLSResolver || record.RequireTLS != plan.RequireTLS {
		return errors.New("runtime route does not match accepted ownership and configuration")
	}
	if record.State != "preparing" && record.State != "published" {
		return errors.New("runtime cannot attest public route readiness")
	}
	if record.State == "published" && record.DeploymentID != r.Deployment.ID {
		return errors.New("runtime published a different deployment")
	}
	for _, destination := range []string{record.Destination, record.PreviousDestination} {
		if destination == "" {
			continue
		}
		parsed, err := url.Parse(destination)
		if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("invalid runtime route destination")
		}
		host, port, err := net.SplitHostPort(parsed.Host)
		number, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || host != "127.0.0.1" || number < 1024 || number > 65535 {
			return errors.New("runtime route destination is not a private candidate")
		}
	}
	if record.State == "published" && record.Destination == "" {
		return errors.New("runtime route lacks its published destination")
	}
	return nil
}
