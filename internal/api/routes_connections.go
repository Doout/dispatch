package api

import (
	"github.com/go-chi/chi/v5"
)

func (a *API) connectionsRoutes(r chi.Router) {
	r.Route("/secrets", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listSecrets)
		r.Get("/usage", a.listSecretUsage)
		r.Post("/", a.createSecret)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/usage/deployments", a.secretUsageDeployments)
			r.Put("/", a.updateSecret)
			a.destructiveRoute(r, "DELETE", "/", "secret", "delete", a.deleteSecret)
		})
	})
	r.Route("/secret-stores", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listSecretStores)
		r.Post("/", a.createSecretStore)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateSecretStore)
			r.Post("/verify", a.verifySecretStore)
			a.destructiveRoute(r, "DELETE", "/", "secret-store", "delete", a.deleteSecretStore)
		})
	})
	r.Route("/private-networks", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listPrivateNetworks)
		r.Post("/", a.createPrivateNetwork)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updatePrivateNetwork)
			r.Post("/verify", a.verifyPrivateNetwork)
			a.destructiveRoute(r, "POST", "/rotate-token", "private-network", "rotate-token", a.rotateEdgeToken)
			a.destructiveRoute(r, "POST", "/revoke", "private-network", "revoke", a.revokeEdgeNode)
			r.Post("/install-connector", a.installLanewayConnector)
			a.destructiveRoute(r, "DELETE", "/", "private-network", "delete", a.deletePrivateNetwork)
		})
	})
	r.Route("/laneway-networks", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/authorize", a.startLanewayAuthorization)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/inventory", a.getLanewayInventory)
			r.Post("/node-installers", a.createLanewayNodeInstaller)
			r.Post("/routes", a.createLanewayRoute)
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/github-apps", a.listGitHubApps)
		r.Post("/github-apps", a.createGitHubApp)
		r.Put("/github-apps/{id}", a.updateGitHubApp)
		a.destructiveRoute(r, "DELETE", "/github-apps/{id}", "github-app", "delete", a.deleteGitHubApp)
		r.Post("/github-apps/{id}/verify", a.verifyGitHubApp)
		r.Get("/github-apps/{id}/installations", a.listGitHubAppInstallations)
		r.Get("/github-apps/{id}/repositories", a.listGitHubAppRepositories)
		r.Post("/github-apps/manifest", a.startGitHubAppManifest)
	})
	r.Route("/servers", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(a.ownerOnly)
			r.Get("/", a.listServers)
			r.Post("/", a.createServer)
		})
		r.Route("/{id}", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(a.ownerOnly)
				r.Put("/", a.updateServer)
				r.Post("/relay/verify", a.verifyRelayServer)
				r.Get("/relay/webhooks", a.listRelayWebhooks)
				r.Post("/relay/webhooks", a.createRelayWebhook)
				a.destructiveRoute(r, "DELETE", "/relay/webhooks/{webhookId}", "relay-webhook", "delete", a.deleteRelayWebhook)
				r.Post("/repair", a.repairOpenShiftServer)
				a.destructiveRoute(r, "DELETE", "/", "server", "delete", a.deleteServer)
			})
			r.With(a.serverPermission).Get("/topology", a.getServerTopology)
		})
	})
	r.Route("/relay", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/ssh/scan", a.scanRelaySSHHost)
		r.Post("/ssh/install", a.installRelayOverSSH)
	})
	r.Route("/builders", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/ssh/scan", a.scanBuilderSSHHost)
	})
}
