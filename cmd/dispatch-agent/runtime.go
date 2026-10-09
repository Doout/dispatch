package main

import (
	"context"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimeclient"
	"net/http"
	"time"
)

var runtimeHeartbeatInterval = 10 * time.Second

type runtimeWorker = runtimeclient.Worker

func runtimeRequest(ctx context.Context, client *http.Client, controller, node, token, method, path string, input, output any) error {
	c := runtimeclient.Client{HTTP: client, Controller: controller, Node: node, Artifact: runningArtifact(), Token: func(context.Context) (string, error) { return token, nil }, ResponseError: responseError}
	return c.Request(ctx, method, path, input, output)
}
func leaseRuntime(ctx context.Context, client *http.Client, controller, node, token string) (*remoteruntime.LeasedJob, error) {
	c := runtimeclient.Client{HTTP: client, Controller: controller, Node: node, Artifact: runningArtifact(), Token: func(context.Context) (string, error) { return token, nil }, ResponseError: responseError}
	return c.Lease(ctx)
}
func executeRuntime(ctx context.Context, client *http.Client, controller, node string, token func(context.Context) (string, error), worker runtimeWorker, job remoteruntime.LeasedJob) error {
	c := runtimeclient.Client{HTTP: client, Controller: controller, Node: node, Artifact: runningArtifact(), Token: token, ResponseError: responseError, HeartbeatInterval: runtimeHeartbeatInterval}
	return c.Execute(ctx, worker, job)
}
