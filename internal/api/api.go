package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/doout/dispatch/internal/analytics"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/drift"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/observe"
	"github.com/doout/dispatch/internal/openshift"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
	workflowservice "github.com/doout/dispatch/internal/workflow"
)

type AuthConfig struct {
	AdminToken string
	Username   string
	Password   string
	PublicURL  string
}

type EventConfig struct {
	BackupDirectory string
	MasterKeyFile   string
	DatabaseURL     string
	WebhookSecret   string
	DefaultCommand  string
	GitHubAPIURL    string
	GitHubToken     string
	Vault           *secretcrypto.Vault
	GitHubApps      *githubapp.Manager
	SecretResolver  *secretvalue.Resolver
	Edge            *edge.Broker
	RepositoryCache string
	Analytics       analytics.Reader
}

type githubEventServices struct {
	events *events.Service
	groups *groups.Service
}

type API struct {
	backupMu           sync.Mutex
	previewReportMu    sync.Mutex
	temporaryPreviewMu sync.Mutex
	observations       *observe.Service
	drift              *drift.Service
	overviewSnapshots  overviewCache
	handler            http.Handler
	store              store.Store
	deploy             *deploy.Service
	demo               bool
	auth               AuthConfig
	logger             *slog.Logger
	events             *events.Service
	groups             *groups.Service
	eventConfig        EventConfig
	secretResolver     *secretvalue.Resolver
	edge               *edge.Broker
	openShift          *openshift.Bootstrapper
	lifecycle          events.Lifecycle
	githubMu           sync.Mutex
	githubServices     map[string]githubEventServices
	manifestMu         sync.Mutex
	manifestStates     map[string]githubAppManifestState
	authManifestStates map[string]authProviderManifestState
	workflows          *workflowservice.Service

	sessionMu   sync.RWMutex
	sessions    map[string]sessionState
	oauthMu     sync.Mutex
	oauthStates map[string]oauthState
	oauthCodes  map[string]oauthCode
}

func New(data store.Store, deployments *deploy.Service, demo bool, auth AuthConfig, logger *slog.Logger, eventConfigs ...EventConfig) *API {
	eventConfig := EventConfig{DefaultCommand: "/preview"}
	if len(eventConfigs) > 0 {
		eventConfig = eventConfigs[0]
		if eventConfig.DefaultCommand == "" {
			eventConfig.DefaultCommand = "/preview"
		}
	}
	if eventConfig.Edge == nil && eventConfig.Vault != nil {
		eventConfig.Edge = edge.New(data, eventConfig.Vault)
	}
	if eventConfig.GitHubApps != nil && eventConfig.GitHubApps.Edge == nil {
		eventConfig.GitHubApps.Edge = eventConfig.Edge
	}
	if eventConfig.SecretResolver == nil && eventConfig.Vault != nil {
		eventConfig.SecretResolver = secretvalue.New(data, eventConfig.Vault)
	}
	if eventConfig.SecretResolver != nil && eventConfig.SecretResolver.Edge == nil {
		eventConfig.SecretResolver.Edge = eventConfig.Edge
	}
	var resolver events.PullRequestResolver
	var groupResolver groups.Resolver
	if eventConfig.GitHubAPIURL != "" || eventConfig.GitHubToken != "" {
		githubResolver := events.GitHubResolver{BaseURL: eventConfig.GitHubAPIURL, Token: eventConfig.GitHubToken}
		resolver, groupResolver = githubResolver, githubResolver
	}
	var notifier events.Notifier
	if eventConfig.GitHubToken != "" {
		notifier = events.GitHubNotifier{BaseURL: eventConfig.GitHubAPIURL, Token: eventConfig.GitHubToken}
	}
	lifecycle := events.DeploymentLifecycle{Store: data, Deployments: deployments}
	groupNotifier, _ := notifier.(groups.Notifier)
	a := &API{
		store:              data,
		deploy:             deployments,
		demo:               demo,
		auth:               auth,
		logger:             logger,
		events:             events.New(data, resolver, lifecycle, notifier),
		groups:             groups.New(data, deployments, groupResolver, groupNotifier, nil),
		eventConfig:        eventConfig,
		secretResolver:     eventConfig.SecretResolver,
		openShift:          openshift.New(),
		lifecycle:          lifecycle,
		edge:               eventConfig.Edge,
		sessions:           make(map[string]sessionState),
		oauthStates:        make(map[string]oauthState),
		oauthCodes:         make(map[string]oauthCode),
		githubServices:     make(map[string]githubEventServices),
		manifestStates:     make(map[string]githubAppManifestState),
		authManifestStates: make(map[string]authProviderManifestState),
	}
	deployments.ConfigureServices(eventConfig.Vault, eventConfig.SecretResolver)
	a.drift = drift.New(data, eventConfig.Vault)
	a.observations = observe.New(data, a.drift, deployments, eventConfig.Vault)
	deployments.OnFinished = a.observations.DeploymentFinished
	a.workflows = workflowservice.NewService(data, eventConfig.GitHubApps, eventConfig.SecretResolver, deployments, logger, eventConfig.RepositoryCache)
	a.handler = a.routes()
	return a
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }

func Shutdown(ctx context.Context, server *http.Server) error { return server.Shutdown(ctx) }
