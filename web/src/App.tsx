import { useDeploymentLogs } from "./deployments/useDeploymentLogs";
import { OperationsPage } from "./OperationsPage";
import { SettingsPage } from "./SettingsPage";
import { DeploymentsPage } from "./deployments/DeploymentsPage";
import { DeploymentCatalogProvider } from "./deployments/DeploymentCatalog";
import { NavigationShortcuts } from "./deployments/NavigationShortcuts";
import { AnalyticsPage } from "./AnalyticsPage";
import { ServicesPage } from "./ServicesPage";
import { subscribeOverview } from "./overviewStream";
import { useCallback, useEffect, useRef, useState } from "react";
import { List, UsersThree } from "@phosphor-icons/react";
import {
  api,
  Deployment,
  Overview,
  Project,
  Server,
  getImpersonatedUserID,
  setImpersonatedUserID,
  setToken,
  User,
} from "./api";
import { AppRoute, readRoute, routePath, View } from "./routes";
import { ApplicationsPage } from "./ApplicationsPage";
import { ConnectionsPage } from "./ConnectionsPage";
import { PageHeader, pageTitles } from "./PageHeader";
import { ServerRuntime } from "./deployments/RuntimeTopology";
import { AccessPage } from "./AccessPage";
import { ChangePasswordDialog } from "./ChangePasswordDialog";
import { AccountProfileDialog } from "./AccountProfileDialog";
import { hasOAuthHandoff } from "./oauthHandoff";
import { canManageAnyProject, canManageController, canManageProject } from "./permissions";
import type { Dialog, DeleteTarget } from "./app/dialogTypes";
import { Nav, AccountMenu, ImpersonationBanner } from "./app/Navigation";
import { canDeleteResource, ResourceDialog, DeleteDialog } from "./app/ResourceDialogs";
import { AuthScreen } from "./app/AuthScreen";
import { UnavailablePage, PageLoading, Mark } from "./components/PageStates";
import { DeploymentDetailsPage, MissingDeploymentPage } from "./deployments/DeploymentDetailsPage";
import { EventsPage } from "./events/EventRulesPage";
import { ServersPage } from "./servers/ServersPage";
import { ProjectsPage } from "./projects/ProjectsPage";
import { SecretsPage } from "./secrets/SecretsPage";

// Keep existing component imports available while feature modules own their state.
export { Nav, AccountMenu, ImpersonationBanner } from "./app/Navigation";
export { DeploymentsPage } from "./deployments/DeploymentsPage";
export { DeploymentDetailsPage, DeploymentQuickView } from "./deployments/DeploymentDetailsPage";
export { ServersPage } from "./servers/ServersPage";
export { SecretsPage } from "./secrets/SecretsPage";
export { DeployForm } from "./deployments/DeployForm";

function currentPageScroll() {
  return window.innerWidth <= 920
    ? window.scrollY
    : (document.getElementById("page-content")?.scrollTop ?? 0);
}

function restorePageScroll(scrollTop: number) {
  if (window.innerWidth <= 920) window.scrollTo(0, scrollTop);
  else {
    const page = document.getElementById("page-content");
    if (page) page.scrollTop = scrollTop;
  }
}

export default function DispatchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [historicalDeployments, setHistoricalDeployments] = useState<Record<string, Deployment>>({});
  const [historicalError, setHistoricalError] = useState("");
  const [route, setRoute] = useState<AppRoute>(() => readRoute());
  const view = route.view;
  const [dialog, setDialog] = useState<Dialog>(null);
  const [creatingApplication, setCreatingApplication] = useState(false);
  const [editingProject, setEditingProject] = useState<Project | null>(null);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [newServerRuntime, setNewServerRuntime] =
    useState<Server["runtime"]>("docker");
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null);
  const [deployAppID, setDeployAppID] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [needsAuth, setNeedsAuth] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);
  const [connectionNotice, setConnectionNotice] = useState("");
  const [changingPassword, setChangingPassword] = useState(false);
  const [viewingAccountProfile, setViewingAccountProfile] = useState(() =>
    new URLSearchParams(window.location.search).has("accountLink"),
  );
  const [accountLinkNotice] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    return params.get("accountLink") === "linked"
      ? params.get("detail") || "Account linked."
      : "";
  });
  const [accountLinkError] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    return params.get("accountLink") === "error"
      ? params.get("detail") || "Account could not be linked."
      : "";
  });
  const connectionCallbackHandled = useRef(false);

  const navigateRoute = useCallback(
    (
      next: AppRoute,
      options: {
        replace?: boolean;
        preserveScroll?: boolean;
        state?: Record<string, unknown>;
      } = {},
    ) => {
      const currentScroll = currentPageScroll();
      window.history.replaceState(
        { ...(window.history.state ?? {}), scrollTop: currentScroll },
        "",
        window.location.href,
      );
      const nextScroll = options.preserveScroll ? currentScroll : 0;
      const currentRoute = readRoute();
      const returnDepth = window.history.state?.deploymentReturnDepth;
      const nextState: Record<string, unknown> = {
        dispatchRoute: true,
        scrollTop: nextScroll,
        ...(options.state ?? {}),
      };
      if (next.view === "deployments" && next.deploymentID && currentRoute.view === "deployments") {
        if (currentRoute.deploymentID && Number.isSafeInteger(returnDepth) && returnDepth > 0)
          nextState.deploymentReturnDepth = returnDepth + (options.replace ? 0 : 1);
        else if (!currentRoute.deploymentID && !options.replace)
          nextState.deploymentReturnDepth = 1;
      }
      if (options.replace)
        window.history.replaceState(nextState, "", routePath(next));
      else window.history.pushState(nextState, "", routePath(next));
      setRoute(next);
      window.requestAnimationFrame(() => restorePageScroll(nextScroll));
    },
    [],
  );

  const load = useCallback(
    async (quiet = false) => {
      try {
        const next = await api.overview();
        setOverview(next);
        setNeedsAuth(false);
        setError("");
        if (
          !quiet &&
          next.identity?.systemRole === "owner" &&
          next.servers.length === 0 &&
          readRoute().view === "deployments"
        )
          navigateRoute({ view: "servers" }, { replace: true });
      } catch (cause) {
        const failure = cause as Error & { status?: number };
        if (
          getImpersonatedUserID() &&
          [403, 404, 409].includes(failure.status ?? 0)
        ) {
          setImpersonatedUserID("");
          try {
            const next = await api.overview();
            setOverview(next);
            setNeedsAuth(false);
            setError("User view ended because that account is unavailable.");
            navigateRoute({ view: "access" }, { replace: true });
          } catch (recoveryCause) {
            const recovery = recoveryCause as Error & { status?: number };
            if (recovery.status === 401) setNeedsAuth(true);
            else setError(recovery.message);
          }
        } else if (failure.status === 401) setNeedsAuth(true);
        else setError(failure.message);
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [navigateRoute],
  );

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const connectionCallback =
      params.has("githubAppSetup") ||
      params.has("githubAppCreated") ||
      params.has("githubAppStatus") ||
	  params.has("lanewayStatus") ||
      params.has("authProviderStatus") ||
      params.has("authProvider") ||
      params.has("installation_id") ||
      hasOAuthHandoff(params);
    const canonicalPath = routePath(readRoute());
    const currentPath = `${window.location.pathname}${window.location.search}`;
    window.history.replaceState(
      {
        ...(window.history.state ?? {}),
        dispatchRoute: true,
        scrollTop: currentPageScroll(),
      },
      "",
      !connectionCallback && currentPath !== canonicalPath
        ? canonicalPath
        : window.location.href,
    );
    const handlePopState = (event: PopStateEvent) => {
      setRoute(readRoute());
      window.requestAnimationFrame(() => {
        const scrollTop =
          typeof event.state?.scrollTop === "number"
            ? event.state.scrollTop
            : 0;
        restorePageScroll(scrollTop);
      });
    };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    void load();

  }, [load]);

  useEffect(() => subscribeOverview(
    (next) => { setOverview(next); setNeedsAuth(false); setError(""); },
    () => { void load(true); },
  ), [load]);

  useEffect(() => {
    if (!overview || needsAuth || connectionCallbackHandled.current) return;
    connectionCallbackHandled.current = true;
    const params = new URLSearchParams(window.location.search);
    const setupID = params.get("githubAppSetup");
    const installationID = Number(params.get("installation_id") || "0");
    const createdID = params.get("githubAppCreated");
    const callbackStatus = params.get("githubAppStatus");
	const lanewayStatus = params.get("lanewayStatus");
	if (lanewayStatus) {
	  navigateRoute({ view: "connections" }, { replace: true });
	  setConnectionNotice(lanewayStatus === "connected" ? "Laneway network connected." : params.get("detail") || "Laneway connection failed.");
	  void load(true);
	} else if (setupID && installationID > 0) {
      navigateRoute({ view: "connections" }, { replace: true });
      void (async () => {
        try {
          const existing = overview.githubApps.find(connection => connection.id === setupID);
          if (!existing?.installationId) {
            await api.updateGitHubApp(setupID, { installationId: installationID });
          }
          await api.verifyGitHubApp(setupID);
          setConnectionNotice("GitHub App installation connected. Existing organizations remain connected.");
          await load(true);
        } catch (cause) {
          setConnectionNotice((cause as Error).message);
        }
      })();
    } else if (createdID) {
      navigateRoute({ view: "connections" }, { replace: true });
      setConnectionNotice(
        "GitHub App created. Install it on an account to finish the connection.",
      );
    } else if (callbackStatus === "error") {
      navigateRoute({ view: "connections" }, { replace: true });
      setConnectionNotice(params.get("detail") || "GitHub App setup failed.");
    }
  }, [load, navigateRoute, needsAuth, overview]);

  // History can include runs older than the overview's bounded inventory.
  const cachedDeployment = route.deploymentID ? historicalDeployments[`${overview?.identity?.id ?? ""}:${route.deploymentID}`] : undefined;
  const routeDeployment = overview?.deployments.find(
    (item) => item.id === route.deploymentID,
  ) ?? (overview && cachedDeployment?.app && canManageProject(overview, cachedDeployment.app.projectId, "project.view") ? cachedDeployment : undefined);
  useEffect(() => { setHistoricalDeployments({}); }, [overview?.identity?.id]);
  useEffect(() => {
    let alive = true; setHistoricalError("");
    if (!overview || !route.deploymentID || routeDeployment) return;
    void api.deployment(route.deploymentID).then(item => {
      if (alive) setHistoricalDeployments(current => ({ ...current, [`${overview?.identity?.id ?? ""}:${item.id}`]: item }));
    }).catch(cause => { if (alive) setHistoricalError(cause.message); });
    return () => { alive = false; };
  }, [route.deploymentID, Boolean(routeDeployment), Boolean(overview), overview?.identity?.id]);
  const routeServer = overview?.servers.find(
    (item) => item.id === route.serverID,
  );
  const canManageInfrastructure = overview
    ? canManageController(overview, "infrastructure.manage")
    : false;
  const canManageSecrets = overview
    ? canManageController(overview, "secrets.manage")
    : false;
  const canManageConnections = overview
    ? canManageController(overview, "connections.manage")
    : false;
  const dialogAuthorized = Boolean(
    overview &&
      dialog &&
      ((dialog === "server" || dialog === "repair")
        ? canManageInfrastructure
        : dialog === "project"
          ? editingProject
            ? canManageProject(
                overview,
                editingProject.id,
                "project.manage",
              )
            : overview.identity?.systemRole === "owner"
          : canManageAnyProject(overview, "deployment.run")),
  );
  const deleteAuthorized = Boolean(
    overview && deleteTarget && canDeleteResource(overview, deleteTarget),
  );
  const selectedDeployment = routeDeployment;
  const { logs, logsLoading, logsError } = useDeploymentLogs(selectedDeployment);

  const navigate = (next: View) => {
    navigateRoute({ view: next });
    if (next !== "applications") setCreatingApplication(false);
    setMobileNav(false);
  };

  const startImpersonating = async (user: User) => {
    setImpersonatedUserID(user.id);
    setViewingAccountProfile(false);
    setChangingPassword(false);
    setLoading(true);
    navigateRoute({ view: "deployments" }, { replace: true });
    await load();
  };

  const stopImpersonating = async () => {
    setImpersonatedUserID("");
    setViewingAccountProfile(false);
    setChangingPassword(false);
    setLoading(true);
    navigateRoute({ view: "access" }, { replace: true });
    await load();
  };

  const logout = async () => {
    try {
      await api.logout();
    } catch {
      // Clearing the local token still ends this browser session.
    } finally {
      setToken("");
      setOverview(null);
      setMobileNav(false);
      setChangingPassword(false);
      setNeedsAuth(true);
      setLoading(false);
    }
  };

  if (needsAuth)
    return (
      <AuthScreen
        onAuthenticated={() => {
          setNeedsAuth(false);
          setLoading(true);
          void load();
        }}
      />
    );

  return (
    <DeploymentCatalogProvider key={overview?.identity?.id ?? "anonymous"} overview={overview} onNavigate={navigateRoute}><div className="shell">
      <a className="skip-link" href="#page-content">
        Skip to content
      </a>
      <Nav
        open={mobileNav}
        view={view}
        overview={overview}
        onClose={() => setMobileNav(false)}
        onNavigate={navigate}
      />

      <main className="workspace">
        <header className="app-bar">
          <div className="app-bar-start">
            <button
              className="menu-button"
              aria-label="Open navigation"
              aria-controls="primary-navigation"
              aria-expanded={mobileNav}
              onClick={() => setMobileNav(true)}
            >
              <List size={22} weight="bold" />
            </button>
            <div className="mobile-brand">
              <Mark />
              <strong>Dispatch</strong>
            </div>
          </div>
          <div className="workspace-location"><span>Workspace</span><span aria-hidden="true">/</span><strong>{pageTitles[view]}</strong></div>
          {overview && <NavigationShortcuts overview={overview} route={route} onNavigate={navigateRoute} />}
          {overview?.identity && (
            <AccountMenu
              identity={overview.identity}
              impersonating={Boolean(overview.impersonator)}
              onOpenProfile={() => setViewingAccountProfile(true)}
              onChangePassword={() => setChangingPassword(true)}
              onLogout={() => void logout()}
            />
          )}
        </header>

        {overview?.impersonator && overview.identity && (
          <ImpersonationBanner
            identity={overview.identity}
            impersonator={overview.impersonator}
            onExit={() => void stopImpersonating()}
          />
        )}

        {error && (
          <div className="error-banner" role="alert">
            <strong>Inventory unavailable</strong>
            <span>{error}</span>
            <button onClick={() => void load()}>Retry</button>
          </div>
        )}

        <div className="page-scroll" id="page-content">
          {loading && <PageLoading />}
          {!loading &&
            overview &&
            view === "deployments" &&
            route.deploymentID &&
            routeDeployment && (
              <DeploymentDetailsPage
                overview={overview}
                deployment={routeDeployment}
                section={route.deploymentSection ?? "summary"}
                onOpenDeployment={id => navigateRoute({ view: "deployments", deploymentID: id })}
                onSelectDeployment={async id => {
                  if (id === routeDeployment.id) return;
                  if (!overview.deployments.some(item => item.id === id) && !historicalDeployments[`${overview.identity?.id ?? ""}:${id}`]) {
                    const item = await api.deployment(id);
                    setHistoricalDeployments(current => ({ ...current, [`${overview.identity?.id ?? ""}:${id}`]: item }));
                  }
                  navigateRoute({ view: "deployments", deploymentID: id, deploymentSection: "history" }, { preserveScroll: true });
                }}
                onSectionChange={(section) =>
                  navigateRoute({
                    view: "deployments",
                    deploymentID: routeDeployment.id,
                    deploymentSection: section,
                  })
                }
                logs={logs}
                logsLoading={logsLoading}
                logsError={logsError}
                onBack={() => {
                  const returnDepth = window.history.state?.deploymentReturnDepth;
                  if (Number.isSafeInteger(returnDepth) && returnDepth > 0)
                    window.history.go(-returnDepth);
                  else
                    navigateRoute({ view: "deployments" }, { replace: true });
                }}
                onCancel={async () => {
                  try {
                    await api.cancel(routeDeployment.id);
                    await load();
                  } catch (cause) {
                    setError((cause as Error).message);
                  }
                }}
                canCancel={Boolean(
                  routeDeployment.app?.projectId &&
                    canManageProject(
                      overview,
                      routeDeployment.app.projectId,
                      "deployment.cancel",
                    ),
                )}
              />
            )}
          {!loading &&
            overview &&
            view === "deployments" &&
            route.deploymentID &&
            !routeDeployment && (
              historicalError ? <MissingDeploymentPage
                onBack={() => navigateRoute({ view: "deployments" }, { replace: true })}
              /> : <p role="status" className="runtime-loading">Loading deployment…</p>
            )}
          {!loading &&
            overview &&
            view === "deployments" &&
            !route.deploymentID && (
              <DeploymentsPage
                overview={overview}
                onChanged={() => load(true)}
                filters={route.deploymentFilters}
                onFilters={filters => navigateRoute({ ...route, deploymentFilters: filters }, { replace: true, preserveScroll: true })}
                selectedApplicationID={route.deploymentApplicationID}
                selectedStageName={route.deploymentStage}
                onSelectStage={(applicationID, stageName) =>
                  navigateRoute(
                    {
                      view: "deployments",
                      deploymentApplicationID: applicationID,
                      deploymentStage: stageName,
                      deploymentFilters: route.deploymentFilters,
                    },
                    { replace: true, preserveScroll: true },
                  )
                }
                onSelect={(id, section) => navigateRoute(
                  { view: "deployments", deploymentID: id, deploymentSection: section },
                  { state: { deploymentReturnDepth: 1 } },
                )}
                onOpen={(next) => {
                  setDeployAppID("");
                  setDialog(next);
                }}
                onCreateApplication={() => {
                  setCreatingApplication(true);
                  navigateRoute({ view: "applications" });
                }}
              />
            )}
          {!loading && overview && view === "applications" && (
            <ApplicationsPage
              overview={overview}
              section={route.applicationSection ?? "applications"}
              applicationID={route.applicationID}
              configurationSourceID={route.configurationSourceID}
              creating={creatingApplication}
              onToggleCreate={() => setCreatingApplication((value) => !value)}
              onChanged={async () => {
                await load();
                setCreatingApplication(false);
              }}
              onDeploy={(appID) => {
                setDeployAppID(appID);
                setDialog("deploy");
              }}
              onDelete={(application) =>
                setDeleteTarget({ kind: "application", item: application })
              }
              onDeleteGroup={(group) =>
                setDeleteTarget({ kind: "previewGroup", item: group })
              }
              onNavigate={navigate}
              onOpenTopology={(resource) =>
                navigateRoute({
                  view: "applications",
                  applicationID: resource.id,
                })
              }
              onOpenConfigurationTopology={(source) =>
                navigateRoute({
                  view: "applications",
                  configurationSourceID: source.id,
                })
              }
              onOpenDeployment={id => navigateRoute({ view: "deployments", deploymentID: id })}
              onOpenWorkflowStage={(applicationID, stageName) =>
                navigateRoute({
                  view: "deployments",
                  deploymentApplicationID: applicationID,
                  deploymentStage: stageName,
                })
              }
              onOpenDeploymentManifests={(deploymentID) =>
                navigateRoute({
                  view: "deployments",
                  deploymentID,
                  deploymentSection: "manifests",
                })
              }
              onCloseTopology={() => navigateRoute({ view: "applications" })}
            />
          )}
          {!loading && overview && view === "events" && (
            <EventsPage
              overview={overview}
              section={route.eventSection ?? "rules"}
              onSectionChange={(section) =>
                navigateRoute({ view: "events", eventSection: section })
              }
              onConfigure={() => navigateRoute({ view: "applications" })}
              onChanged={async () => {
                await load();
              }}
            />
          )}
          {!loading && overview && view === "projects" && (
            <ProjectsPage
              overview={overview}
              canCreate={overview.identity?.systemRole === "owner"}
              onAdd={() => {
                setEditingProject(null);
                setDialog("project");
              }}
              onEdit={(project) => {
                setEditingProject(project);
                setDialog("project");
              }}
              onDelete={(project) =>
                setDeleteTarget({ kind: "project", item: project })
              }
            />
          )}
          {!loading &&
            overview &&
            view === "servers" &&
            route.serverID &&
            routeServer && <ServerRuntime server={routeServer} />}
          {!loading &&
            overview &&
            view === "servers" &&
            route.serverID &&
            !routeServer && (
              <UnavailablePage
                view="servers"
                title="Server unavailable"
                body="This server does not exist or is outside your project access."
                onBack={() =>
                  navigateRoute({ view: "servers" }, { replace: true })
                }
              />
            )}
          {!loading && overview && view === "settings" && <SettingsPage key={overview.identity?.id} overview={overview} onChanged={() => load(true)} />}
          {!loading && overview && view === "operations" && <OperationsPage key={overview.identity?.id} overview={overview} filters={route.operationsFilters ?? {}} onFilters={filters => navigateRoute({view: "operations", operationsFilters: filters}, {replace: true, preserveScroll: true})} onNavigate={navigateRoute} onChanged={() => load(true)} />}
          {!loading && overview && view === "analytics" && <AnalyticsPage key={overview.identity?.id} overview={overview} filters={route.analyticsFilters ?? {}} onFilters={filters => navigateRoute({view: "analytics", analyticsFilters: filters}, {replace: true, preserveScroll: true})} onNavigate={navigateRoute} />}
          {!loading && overview && view === "servers" && !route.serverID && (
            <ServersPage
              overview={overview}
              canManage={canManageInfrastructure}
              onTopology={(server) =>
                navigateRoute({ view: "servers", serverID: server.id })
              }
              onChanged={async () => {
                await load();
              }}
              onAdd={() => {
                setEditingServer(null);
                setNewServerRuntime("docker");
                setDialog("server");
              }}
              onEdit={(server) => {
                setEditingServer(server);
                setDialog("server");
              }}
              onRepair={(server) => {
                setEditingServer(server);
                setDialog("repair");
              }}
              onDelete={(server) =>
                setDeleteTarget({ kind: "server", item: server })
              }
            />
          )}
          {!loading && overview && view === "services" && <ServicesPage overview={overview} onChanged={load} />}
          {!loading && overview && view === "secrets" && canManageSecrets && (
            <SecretsPage
              overview={overview}
              onChanged={async () => {
                await load();
              }}
              onDelete={(secret) =>
                setDeleteTarget({ kind: "secret", item: secret })
              }
            />
          )}
          {!loading && overview && view === "secrets" && !canManageSecrets && (
            <UnavailablePage
              view="secrets"
              title="Variables unavailable"
              body="Controller owner access is required."
            />
          )}
          {!loading && overview && view === "connections" && canManageConnections && (
            <ConnectionsPage
              overview={overview}
              selectedConnectionID={route.connectionID}
              onOpenConnection={(id) => navigateRoute({ view: "connections", connectionID: id ?? undefined })}
              notice={connectionNotice}
              onNotice={setConnectionNotice}
              onChanged={async () => {
                await load();
              }}
              onAddRelay={() => {
                setEditingServer(null);
                setNewServerRuntime("relay");
                setDialog("server");
              }}
            />
          )}
          {!loading &&
            overview &&
            view === "connections" &&
            !canManageConnections && (
              <UnavailablePage
                view="connections"
                title="Connections unavailable"
                body="Controller owner access is required."
              />
            )}
          {!loading &&
            overview &&
            view === "access" &&
            overview.identity?.systemRole === "owner" && (
              <AccessPage
                identityID={overview.identity.id}
                onChangePassword={() => setChangingPassword(true)}
                onImpersonate={(user) => void startImpersonating(user)}
              />
            )}
          {!loading &&
            overview &&
            view === "access" &&
            overview.identity?.systemRole !== "owner" && (
              <div className="page-layout">
                <PageHeader view="access" />
                <section className="empty-state">
                  <UsersThree size={31} />
                  <div>
                    <h2>Controller owner required</h2>
                    <p>
                      Your project role does not include user and team
                      management.
                    </p>
                  </div>
                </section>
              </div>
            )}
        </div>
      </main>

      {dialog && overview && dialogAuthorized && (
        <ResourceDialog
          kind={dialog}
          overview={overview}
          project={editingProject ?? undefined}
          server={editingServer ?? undefined}
          serverRuntime={newServerRuntime}
          deployAppID={deployAppID}
          onClose={() => setDialog(null)}
          onChanged={async () => {
            await load();
            setDialog(null);
          }}
          onDeployed={async (id) => {
            setDialog(null);
            await load();
            navigateRoute({ view: "deployments", deploymentID: id });
          }}
        />
      )}
      {deleteTarget && overview && deleteAuthorized && (
        <DeleteDialog
          target={deleteTarget}
          overview={overview}
          onClose={() => setDeleteTarget(null)}
          onDeleted={async () => {
            await load();
            setDeleteTarget(null);
          }}
        />
      )}
      {changingPassword && (
        <ChangePasswordDialog onClose={() => setChangingPassword(false)} onChanged={() => void logout()} />
      )}
      {viewingAccountProfile && (
        <AccountProfileDialog
          readOnly={Boolean(overview?.impersonator)}
          notice={accountLinkNotice}
          initialError={accountLinkError}
          onChangePassword={() => {
            setViewingAccountProfile(false);
            setChangingPassword(true);
          }}
          onClose={() => setViewingAccountProfile(false)}
        />
      )}
    </div></DeploymentCatalogProvider>
  );
}
