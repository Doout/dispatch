import { App as AppModel, PreviewGroup, Project, Secret, Server } from "../api";

export type Dialog = "deploy" | "project" | "server" | "repair" | null;

export type DeleteTarget =
  | { kind: "project"; item: Project }
  | { kind: "server"; item: Server }
  | { kind: "application"; item: AppModel }
  | { kind: "previewGroup"; item: PreviewGroup }
  | { kind: "secret"; item: Secret };
