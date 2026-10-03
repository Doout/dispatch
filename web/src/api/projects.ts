import { request, destructiveRequest } from "./transport";

export type Project = {
  id: string;
  name: string;
  description: string;
  createdAt: string;
};

export const projectsApi = {
  createProject: (body: { name: string; description: string }) =>
    request<Project>("/api/v1/projects", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateProject: (id: string, body: { name: string; description: string }) =>
    request<Project>(`/api/v1/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteProject: (id: string) =>
    destructiveRequest<void>(`/api/v1/projects/${id}`, { method: "DELETE" }),
};
