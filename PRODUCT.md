# Product

<!-- impeccable:product-schema 1 -->

## Platform

Web

## Users

Dispatch is for infrastructure and application teams that deploy to private Docker, Kubernetes, and OpenShift targets. Controller owners manage access. Project roles limit what members and teams can view or change.

## Purpose

Dispatch connects source repositories to deployment targets. It records the source revision, configuration, logs, outputs, and runtime resources for each deployment.

A working deployment path lets an operator:

1. Connect a source and target.
2. Deploy an exact revision.
3. Inspect each build and deployment step.
4. Reuse the same revision when promoting it to another stage.
5. Diagnose a failed release without bypassing Dispatch.

## Boundaries

- The controller ships as one Go service with an embedded React application.
- SQLite is the default store. PostgreSQL is supported.
- Docker, Kubernetes, and OpenShift are deployment targets.
- Agents poll the controller over outbound connections. SSH is limited to installation and recovery.
- Provider integrations use versioned contracts instead of provider-specific code in the core.
- Local secrets stay encrypted and write-only. External secret references resolve when a job starts.
- Repository polling works without a public webhook endpoint. Webhooks and a durable relay can reduce update latency.

## Product rules

1. Show the source, target, state, and result of every deployment.
2. Keep deployment evidence after a failure.
3. Require an explicit action for privileged or destructive work.
4. Do not fall back to a more privileged execution path.
5. Keep provider credentials scoped to the connection that uses them.
6. Make every permission check in the API. The interface may hide unavailable actions, but it is not an authorization boundary.

## Language

Use the product name Dispatch. Write short labels that name the action or state. Avoid marketing copy, repeated helper text, and generic status messages.

## Accessibility

The interface must support keyboard navigation, visible focus, narrow screens, high contrast, and reduced motion. Loading, empty, disconnected, failed, and recovery states must remain usable without color alone.
