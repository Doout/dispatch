package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) workflowsRoutes(r chi.Router) {
	r.Route("/config-sources", func(r chi.Router) {
		r.Get("/", a.listConfigSources)
		r.With(a.ownerOnly).Post("/", a.createConfigSource)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(a.configSourcePermission(core.PermissionProjectConfigure))
			r.Put("/", a.updateConfigSource)
			r.Post("/sync", a.syncConfigSource)
			r.Delete("/", a.deleteConfigSource)
		})
	})
	r.Route("/workflow", func(r chi.Router) {
		r.Post("/validate", a.validateWorkflowDocument)
		r.Route("/resources", func(r chi.Router) {
			r.Get("/", a.listWorkflowResources)
			r.Route("/{id}", func(r chi.Router) {
				r.With(a.workflowResourcePermission(core.PermissionProjectView)).Get("/topology", a.getWorkflowTopology)
				r.Group(func(r chi.Router) {
					r.Use(a.workflowResourcePermission(core.PermissionProjectConfigure))
					r.Post("/activate", a.activateWorkflowResource)
					r.Post("/deactivate", a.deactivateWorkflowResource)
				})
				r.With(a.workflowResourcePermission(core.PermissionDeploymentRun)).Post("/runs", a.runWorkflowResource)
			})
		})
		r.Route("/preview-templates", func(r chi.Router) {
			r.Use(a.ownerOnly)
			r.Get("/", a.listWorkflowPreviewTemplates)
			r.Post("/", a.createWorkflowPreviewTemplate)
			r.Route("/{id}", func(r chi.Router) {
				r.Put("/", a.updateWorkflowPreviewTemplate)
				r.Post("/sync", a.syncWorkflowPreviewTemplate)
				r.Delete("/", a.deleteWorkflowPreviewTemplate)
			})
		})
		r.Route("/temporary-resources", func(r chi.Router) {
			r.Use(a.ownerOnly)
			r.Post("/", a.createTemporaryWorkflowResource)
			r.Post("/import", a.importWorkflowPreviewDocument)
			r.Route("/{id}", func(r chi.Router) {
				r.Put("/", a.updateTemporaryWorkflowResource)
				r.Delete("/", a.deleteTemporaryWorkflowResource)
				r.Post("/preview-trigger", a.createWorkflowPreviewTrigger)
			})
		})
		r.Route("/preview-triggers", func(r chi.Router) {
			r.Use(a.ownerOnly)
			r.Get("/", a.listWorkflowPreviewTriggers)
			r.Route("/{id}", func(r chi.Router) {
				r.Put("/", a.updateWorkflowPreviewTrigger)
				r.Put("/url", a.updateWorkflowPreviewTriggerURL)
			})
		})
		r.Route("/revisions", func(r chi.Router) {
			r.Get("/", a.listWorkflowRevisions)
			r.Route("/{id}", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(a.workflowRevisionPermission(core.PermissionProjectView))
					r.Get("/", a.getWorkflowRevision)
					r.Get("/jobs", a.listWorkflowJobs)
					r.Get("/logs/watch", a.watchWorkflowLogs)
					r.Get("/stages", a.listWorkflowStages)
				})
				r.With(a.ownerOnly).Post("/preview-report", a.reportWorkflowPreview)
			})
		})
		r.Route("/stages/{id}", func(r chi.Router) {
			r.Use(a.workflowStagePermission(core.PermissionStageApprove))
			r.Post("/approve", a.approveWorkflowStage)
		})
	})
}
