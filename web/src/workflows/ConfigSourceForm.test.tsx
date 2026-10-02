// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, type ConfigSource, type GitHubRepository, type Overview } from "../api";
import { WorkflowConfigSourceForm } from "./ConfigSourceForm";

const source = {id: "config", projectId: "p", githubAppId: "github", name: "Production configuration", repository: "org/old", repositoryId: 42, branch: "release", path: ".dispatch", syncMode: "poll", pollIntervalSeconds: 300, active: true, state: "invalid", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z", repositoryStatus: {state: "renamed", repositoryId: 42, fullName: "org/current", detail: "The repository moved to org/current.", recovery: "Review and apply the current name.", checkedAt: "2026-10-01T00:00:00Z"}} as ConfigSource;
const repository = {id:42, fullName:"org/current",name:"current",owner:"org",defaultBranch:"main",private:true,webUrl:"https://github.example/org/current"} as GitHubRepository;
const overview = {projects:[{id:"p",name:"Platform"}],githubApps:[{id:"github",name:"GitHub",state:"ready"}],secrets:[]} as unknown as Overview;
beforeEach(() => {
 vi.spyOn(api,"configSourceRepositories").mockResolvedValue([repository]);
 vi.spyOn(api,"configSourceBranches").mockResolvedValue([{name:"release",sha:"abc",protected:true}]);
 vi.spyOn(api,"updateConfigSource").mockResolvedValue(source);
 vi.spyOn(api,"checkConfigSourceRepository").mockResolvedValue(source);
});
afterEach(()=>{cleanup();vi.restoreAllMocks()});
function show(item=source){render(<WorkflowConfigSourceForm overview={overview} source={item} onCancel={()=>{}} onSaved={async()=>{}}/>)}
it("keeps the repository and branch until an explicit rename is saved",async()=>{
 show();await waitFor(()=>expect(api.configSourceRepositories).toHaveBeenCalledWith("config"));
 expect((screen.getByLabelText("Repository",{selector:"input"}) as HTMLInputElement).value).toBe("org/old");
 expect((screen.getByLabelText("Branch",{selector:"input"}) as HTMLInputElement).value).toBe("release");
 expect(api.updateConfigSource).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Use org/current"}));
 await waitFor(()=>expect(api.configSourceBranches).toHaveBeenCalledWith("config","org/current",42));
 expect((screen.getByLabelText("Branch",{selector:"input"}) as HTMLInputElement).value).toBe("release");
 fireEvent.click(screen.getByRole("button",{name:"Save changes"}));
 await waitFor(()=>expect(api.updateConfigSource).toHaveBeenCalledWith("config",expect.objectContaining({repository:"org/current",repositoryId:42,branch:"release"})));
});
it("shows empty and failed repository loading without clearing saved selection",async()=>{
 vi.mocked(api.configSourceRepositories).mockRejectedValueOnce(new Error("Installation unavailable"));show();
 await screen.findByText("Installation unavailable");
 expect((screen.getByLabelText("Repository",{selector:"input"}) as HTMLInputElement).value).toBe("org/old");
 vi.mocked(api.configSourceRepositories).mockResolvedValue([]);
 fireEvent.click(screen.getByRole("button",{name:"Reload repositories"}));
 await screen.findByText(/No accessible repositories/);
 expect((screen.getByLabelText("Branch",{selector:"input"}) as HTMLInputElement).value).toBe("release");
});
it("does not replace a selected branch when branch loading fails",async()=>{
 vi.mocked(api.configSourceBranches).mockRejectedValue(new Error("Contents permission missing"));
 show({...source,repository:"org/current",repositoryStatus:undefined});
 await screen.findByText(/Contents permission missing/);
 expect((screen.getByLabelText("Branch",{selector:"input"}) as HTMLInputElement).value).toBe("release");
});
it("blocks an archived selection and shows recovery guidance",async()=>{
 vi.mocked(api.configSourceRepositories).mockResolvedValue([{...repository,archived:true}]);
 show({...source,repository:"org/current",repositoryStatus:{state:"archived",repositoryId:42,fullName:"org/current",detail:"This repository is archived.",recovery:"Unarchive it in GitHub.",checkedAt:"2026-10-01T00:00:00Z"}});
 await screen.findByText(/Repository ID 42 · Archived/);
 expect((screen.getByRole("button",{name:"Save changes"}) as HTMLButtonElement).disabled).toBe(true);
 expect(api.configSourceBranches).not.toHaveBeenCalled();
});
it("requires selecting a replacement when a repository name is reused", async () => {
 vi.mocked(api.configSourceRepositories).mockResolvedValue([{...repository,id:99}]);
 show({...source,repository:"org/current",repositoryStatus:undefined});
 await screen.findByText(/The saved source uses repository 42/);
 expect((screen.getByRole("button",{name:"Save changes"}) as HTMLButtonElement).disabled).toBe(true);
 expect(api.updateConfigSource).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Select replacement repository 99"}));
 fireEvent.click(screen.getByRole("button",{name:"Save changes"}));
 await waitFor(()=>expect(api.updateConfigSource).toHaveBeenCalledWith("config",expect.objectContaining({repository:"org/current",repositoryId:99,branch:"release"})));
});
