# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Dispatch is initially for one infrastructure operator, authenticated as the GitHub user `doout`, who deploys and operates personal applications on private infrastructure.

## Product Purpose

Dispatch is a small, self-hosted deployment control plane. It connects source repositories to Docker hosts, records an immutable deployment history, and exposes the evidence needed to deploy, inspect, recover, and roll back an application confidently.

Success means the operator can connect a target, define an app, deploy an exact commit, see every transition, and recover without bypassing the control plane.

## Positioning

Dispatch keeps infrastructure-specific provisioning behind versioned provider contracts while the public core stays small. It is designed around one operator's environment rather than broad hosted-PaaS parity or multi-tenant administration.

## Operating Context

The operator works with GitHub repositories, Dockerfiles, Compose files, Docker hosts, Traefik routes, wildcard certificates, deployment logs, private infrastructure APIs, and—later—K3s or Kubernetes clusters and pull-request environments.

## Capabilities and Constraints

- The source lives in one repository and ships as a Go controller, an embedded React interface, and a small agent.
- SQLite WAL is the default store; PostgreSQL is an interchangeable, tested option.
- Docker is the first runtime. K3s and Kubernetes follow through a runtime-driver interface.
- Servers are enrolled through SSH and normally controlled through an outbound authenticated agent connection.
- Private infrastructure logic is delivered as a private sidecar or container implementing the public provider interface.
- The repository and GitHub project remain private until the owner explicitly approves public release.
- Dispatch is single-controller and single-admin in its first releases.

## Brand Commitments

The product name is Dispatch. Its language is concise, operational, and factual. The interface treats deployments as dispatch records moving through explicit stages; it must not imitate Dokploy branding or present itself as a Dokploy fork.

## Evidence on Hand

- The confirmed implementation plan in the originating Codex task.
- No customer claims, benchmarks, pricing, testimonials, or production reliability claims exist and none may be fabricated.
- Any sample state must be visibly identified as demo data.

## Product Principles

1. Show the exact source, target, state, and consequence of every deployment.
2. Prefer one small built-in path and versioned extension points over bundled provider breadth.
3. Make recovery explicit; never silently fall back to a more privileged execution path.
4. Keep secrets write-only and operational decisions auditable.
5. Ship one complete deployment path before expanding into previews, databases, or clusters.

## Accessibility & Inclusion

The web interface must remain keyboard accessible, responsive, high contrast, and usable with reduced motion. Loading, empty, disconnected, failed, and recovery states are first-class product states.
