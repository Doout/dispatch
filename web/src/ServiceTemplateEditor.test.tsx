// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { parse } from "yaml";
import { api, type Overview, type ServiceTemplate } from "./api";
import { ServicesPage } from "./ServicesPage";
import { ServiceTemplateEditor } from "./ServiceTemplateEditor";

const overview: Overview = { demo: false, secretStorageConfigured: true, identity: { id: "owner", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] }, projects: [{ id: "p", name: "Project", description: "", createdAt: "" }], services: [], apps: [], servers: [], deployments: [], eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [] };
const template: ServiceTemplate = { id: "template", name: "new-database", projectId: "p", description: "Create a database", serviceType: "postgresql", inputs: {}, outputs: { connectionUrl: { sensitive: true } }, configSha: "sha", managedBy: "dispatch", revision: 2 };
const document = `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: new-database
spec:
  serviceType: postgresql
  description: Create a database
  sources:
    provisioner:
      repository: example/infrastructure
      branch: main
  inputs:
    database:
      type: string
      required: true
      description: Database name
  provision:
    runFrom: provisioner
    run: ./create-database.sh
    secrets:
      PROVIDER_TOKEN:
        secretRef: provider-token
        key: token
  outputs:
    connectionUrl:
      sensitive: true
`;
beforeEach(() => { vi.spyOn(api, "serviceTemplates").mockResolvedValue([]); vi.spyOn(api, "serviceProvisionRuns").mockResolvedValue([]); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("creates a template from Add service without a repository connection", async () => {
 const save = vi.spyOn(api, "saveServiceTemplate").mockResolvedValue({ id: "template", revision: 1 });
 render(<ServicesPage overview={overview} onChanged={async () => {}} />);
 const user = userEvent.setup();
 await user.click(screen.getByRole("button", { name: "Add service" }));
 await user.click(screen.getByRole("button", { name: "Create template" }));
 await user.type(screen.getByLabelText("Template name"), "new-database");
 await user.selectOptions(screen.getByLabelText("Provisioner"), "script");
 await user.click(screen.getByRole("button", { name: "Add input" }));
 await user.clear(screen.getByLabelText("Input 1 name"));
 await user.type(screen.getByLabelText("Input 1 name"), "database");
 await user.type(screen.getByLabelText("Input 1 label"), "Database name");
 await user.click(screen.getByRole("button", { name: "Add input" }));
 await user.selectOptions(screen.getByLabelText("Input 2 type"), "service");
 fireEvent.change(screen.getByLabelText("Script"), { target: { value: "./create-database.sh" } });
 await user.click(screen.getByRole("button", { name: "Save template" }));
 await waitFor(() => expect(save).toHaveBeenCalledOnce());
 expect(save.mock.calls[0][0]).toBeUndefined();
 const body = save.mock.calls[0][1];
 expect(body.configSourceId).toBe("");
 expect(body.projectId).toBe("p");
 const value = parse(body.document);
 expect(value.kind).toBe("ServiceTemplate");
 expect(value.spec.inputs.database).toEqual({ label: "Database name", required: true, type: "string" });
 expect(value.spec.inputs.input2).toEqual({ required: true, type: "service", serviceType: "postgresql" });
 expect(value.spec.outputs.connectionUrl.sensitive).toBe(true);
 await waitFor(() => expect(screen.getByRole("button", { name: "Templates" }).getAttribute("aria-pressed")).toBe("true"));
});

it("preserves repository sources and secret references when editing in the form", async () => {
 vi.spyOn(api, "serviceTemplate").mockResolvedValue({ ...template, document, configSourceId: "config" });
 const save = vi.spyOn(api, "saveServiceTemplate").mockResolvedValue({ id: "template", revision: 3 });
 const saved = vi.fn(async () => {});
 render(<ServiceTemplateEditor item={template} overview={{ ...overview, configSources: [{ id: "config", projectId: "p", name: "Infrastructure", repository: "example/config" } as NonNullable<Overview["configSources"]>[number]] }} initialProject="" onBack={() => {}} onSaved={saved} />);
 const user = userEvent.setup();
 await user.clear(await screen.findByLabelText("Description"));
 await user.type(screen.getByLabelText("Description"), "Updated description");
 await user.click(screen.getByRole("button", { name: "YAML" }));
 expect((screen.getByLabelText("Template YAML") as HTMLTextAreaElement).value).toContain("secretRef: provider-token");
 await user.click(screen.getByRole("button", { name: "Form" }));
 await user.click(screen.getByRole("button", { name: "Save template" }));
 await waitFor(() => expect(saved).toHaveBeenCalledOnce());
 const body = save.mock.calls[0][1];
 expect(body.revision).toBe(2);
 expect(body.configSourceId).toBe("config");
 const value = parse(body.document);
 expect(value.spec.description).toBe("Updated description");
 expect(value.spec.sources.provisioner.repository).toBe("example/infrastructure");
 expect(value.spec.provision.secrets.PROVIDER_TOKEN).toEqual({ secretRef: "provider-token", key: "token" });
 expect(value.spec.provision.runFrom).toBe("provisioner");
 expect(value.spec.inputs.database.description).toBe("Database name");
});

it("keeps YAML errors visible without losing the document", async () => {
 render(<ServiceTemplateEditor overview={overview} initialProject="p" onBack={() => {}} onSaved={async () => {}} />);
 const user = userEvent.setup();
 await user.click(screen.getByRole("button", { name: "YAML" }));
 fireEvent.change(screen.getByLabelText("Template YAML"), { target: { value: "invalid: [" } });
 await user.click(screen.getByRole("button", { name: "Form" }));
 expect(screen.getByRole("alert")).toBeTruthy();
 expect((screen.getByLabelText("Template YAML") as HTMLTextAreaElement).value).toBe("invalid: [");
});

it("blocks saves if the existing template could not be loaded", async () => {
 vi.spyOn(api, "serviceTemplate").mockRejectedValue(new Error("Template unavailable"));
 const save = vi.spyOn(api, "saveServiceTemplate");
 render(<ServiceTemplateEditor item={template} overview={overview} initialProject="p" onBack={() => {}} onSaved={async () => {}} />);
 expect((await screen.findByRole("alert")).textContent).toContain("Template unavailable");
 await userEvent.setup().click(screen.getByRole("button", { name: "Save template" }));
 expect(save).not.toHaveBeenCalled();
});

it("lists saved and GitOps templates with deletion only for saved definitions", async () => {
 vi.mocked(api.serviceTemplates).mockResolvedValue([template, { ...template, id: "gitops", name: "Repository template", managedBy: "gitops", revision: undefined }]);
 const remove = vi.spyOn(api, "deleteServiceTemplate").mockRejectedValue(new Error("Template changed. Reload and try again."));
 render(<ServicesPage overview={overview} onChanged={async () => {}} />);
 const user = userEvent.setup();
 await user.click(screen.getByRole("button", { name: "Templates" }));
 expect(await screen.findByText("Saved in Dispatch")).toBeTruthy();
 expect(screen.getByText("GitOps")).toBeTruthy();
 expect(screen.getAllByRole("button", { name: "Delete template" })).toHaveLength(1);
 await user.click(screen.getByRole("button", { name: "Delete template" }));
 expect(screen.getByText(/Services already created from it will remain/)).toBeTruthy();
 await user.click(screen.getByRole("button", { name: "Confirm delete" }));
 expect(remove).toHaveBeenCalledWith("template", 2);
 expect((await screen.findByRole("alert")).textContent).toContain("Template changed");
});

it("shows repository templates without a save action", async () => {
 vi.spyOn(api, "serviceTemplate").mockResolvedValue({ ...template, managedBy: "gitops", document });
 render(<ServiceTemplateEditor item={{ ...template, managedBy: "gitops" }} overview={overview} initialProject="" onBack={() => {}} onSaved={async () => {}} />);
 await screen.findByText(/Edit its YAML there/);
 expect(screen.queryByRole("button", { name: "Save template" })).toBeNull();
});

const servers: Overview["servers"] = [
 { id: "docker", name: "Local Docker", address: "local", runtime: "docker", state: "ready", agentMode: "local", createdAt: "" },
 { id: "cluster", name: "Development cluster", address: "https://cluster.example.test", runtime: "kubernetes", state: "ready", agentMode: "local", createdAt: "" },
 { id: "builder", name: "Builder", address: "ssh://builder.example.test", runtime: "builder", state: "ready", agentMode: "local", createdAt: "" },
];

it("creates a PostgreSQL Docker template with only a name and server", async () => {
 const save = vi.spyOn(api, "saveServiceTemplate").mockResolvedValue({ id: "template", revision: 1 });
 render(<ServiceTemplateEditor overview={{ ...overview, servers }} initialProject="p" onBack={() => {}} onSaved={async () => {}} />);
 const user = userEvent.setup();
 expect(screen.queryByLabelText("Script")).toBeNull();
 expect((screen.getByLabelText("Provisioner") as HTMLSelectElement).value).toBe("docker");
 expect(screen.queryByRole("option", { name: "Builder" })).toBeNull();
 await user.type(screen.getByLabelText("Template name"), "postgres-docker");
 await user.selectOptions(screen.getByLabelText("Target server"), "docker");
 await user.click(screen.getByRole("button", { name: "Save template" }));
 await waitFor(() => expect(save).toHaveBeenCalledOnce());
 const doc = parse(save.mock.calls[0][1].document);
 expect(doc.spec.provision).toEqual({ docker: { serverRef: "docker" } });
 expect(doc.spec.inputs).toEqual({});
 expect(doc.spec.outputs).toEqual({ connectionUrl: { sensitive: true } });
});

it("creates a Helm template without scripts or provider code", async () => {
 const save = vi.spyOn(api, "saveServiceTemplate").mockResolvedValue({ id: "template", revision: 1 });
 render(<ServiceTemplateEditor overview={{ ...overview, servers }} initialProject="p" onBack={() => {}} onSaved={async () => {}} />);
 const user = userEvent.setup();
 await user.type(screen.getByLabelText("Template name"), "postgres-helm");
 await user.selectOptions(screen.getByLabelText("Provisioner"), "helm");
 await user.selectOptions(screen.getByLabelText("Target server"), "cluster");
 await user.type(screen.getByLabelText("Namespace"), "databases");
 expect(screen.queryByLabelText("Script")).toBeNull();
 await user.click(screen.getByRole("button", { name: "Save template" }));
 await waitFor(() => expect(save).toHaveBeenCalledOnce());
 expect(parse(save.mock.calls[0][1].document).spec.provision).toEqual({ helm: { serverRef: "cluster", namespace: "databases" } });
});

it("edits a built-in template and preserves advanced YAML settings", async () => {
 const doc = `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata: {name: postgres-docker}
spec:
 serviceType: postgresql
 provision:
  docker:
   serverRef: docker
   network: application-network
   storageMountPath: /var/lib/postgresql/data
   environment: {TZ: UTC}
`;
 vi.spyOn(api, "serviceTemplate").mockResolvedValue({ ...template, document: doc });
 const save = vi.spyOn(api, "saveServiceTemplate").mockResolvedValue({ id: "template", revision: 3 });
 render(<ServiceTemplateEditor item={template} overview={{ ...overview, servers }} initialProject="p" onBack={() => {}} onSaved={async () => {}} />);
 const user = userEvent.setup();
 await user.type(await screen.findByLabelText("Description"), "Updated");
 await user.click(screen.getByRole("button", { name: "Save template" }));
 await waitFor(() => expect(save).toHaveBeenCalledOnce());
 const value = parse(save.mock.calls[0][1].document);
 expect(value.spec.provision.docker).toEqual({ serverRef: "docker", network: "application-network", storageMountPath: "/var/lib/postgresql/data", environment: { TZ: "UTC" } });
 expect(value.spec.provision.run).toBeUndefined();
});

it("saves Neon with a project provider and schema-only data", async () => {
 vi.spyOn(api,"neonProviders").mockResolvedValue([{id:"neon-p",projectId:"p",name:"Preview provider",endpoint:"https://neon.example/api/v2",neonProjectId:"cloud-project",parentBranchId:"production",credentialRef:"key",createdAt:""}]);
 const save=vi.spyOn(api,"saveServiceTemplate").mockResolvedValue({id:"template",revision:1});
 render(<ServiceTemplateEditor overview={overview} initialProject="p" onBack={()=>{}} onSaved={async()=>{}} />);
 const user=userEvent.setup();
 await user.type(screen.getByLabelText("Template name"),"preview-db");
 await user.selectOptions(screen.getByLabelText("Provisioner"),"neon");
 await screen.findByRole("option",{name:"Preview provider"});
 await user.selectOptions(screen.getByLabelText("Project provider"),"neon-p");
 await user.click(screen.getByRole("button",{name:"Save template"}));
 await waitFor(()=>expect(save).toHaveBeenCalledOnce());
 const document=parse(save.mock.calls[0][1].document);
 expect(document.spec.provision.neon).toEqual({providerRef:"neon-p",database:"neondb",dataMode:"schema-only"});
 expect(document.spec.provision.neon.credentialRef).toBeUndefined();
});
