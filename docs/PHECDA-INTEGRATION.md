# ISC Phecda Integration Boundary

This document defines the boundary between ISC-Core and ISC Phecda. It is a design contract for the next API extension; it does not make ISC-Core depend on the GUI repository.

## Responsibilities

ISC-Core remains responsible for machine-facing public access:

- DNS credentials, zones and records
- Dynamic DNS tasks and address monitoring
- Reverse proxy routes
- ACME certificates and renewal
- Reachability probes, firewall plans and rollback
- External verification sessions
- Kernel jobs, audit records and events

ISC Phecda is responsible for user application deployment:

- Project source selection and read-only source scanning
- Stack and framework evidence
- Runtime downloads, checksums and local caches
- Dependency installation and build processes
- Application and container lifecycle
- Health checks, logs, local port allocation and deployment recovery
- Presets and custom server commands

The GUI remains a binary C ABI consumer. It must not import Go packages or internal ISC-Core types.

## Server creation modes

Non-Docker projects use a source directory, archive or Git source and are scanned without executing source-provided commands. Docker is a separate mode and does not require a source directory by default. Its inputs are one of:

- Compose file
- Dockerfile directory
- Existing image reference
- Simple Docker command
- Explicit advanced Docker command

Docker host networking, privileged mode and arbitrary host mounts require explicit confirmation in Phecda. ISC-Core does not install Docker Desktop or manage container internals.

## Public binding

The relationship is intentionally explicit:

```text
PhecdaProject -> PhecdaDeployment -> ISC public binding
```

A public binding references a deployment's local listener and is materialized through existing ISC-Core DDNS, proxy, certificate and verification APIs. A DNS record, proxy route or certificate is not itself a Phecda project.

## Future API extension

When implementation begins, extend `api/openapi.yaml` first and regenerate the Go server contract. Phecda-specific endpoints should be grouped separately from existing DNS and public-access tags:

- project metadata and source references
- read-only scan results and evidence
- preset catalog and versioned runtime requirements
- deployment state and local listener binding
- deployment job status, cancellation and events

Long-running operations must use the existing job/event semantics. Secrets must remain references to the platform secret store and never be returned in project exports or scan logs.

Runtime downloads and process supervision should initially remain in a Phecda Supervisor component. ISC-Core should only gain APIs needed to persist or expose a public binding, not arbitrary shell execution or package-manager behavior.

## Product boundaries

ISC Mizar is a future remote monitoring client and requires a separate authenticated remote-management service. ISC Dubhe is a future cluster control plane and requires agents, node identity, scheduling and multi-node state. Neither product is enabled by this local API boundary.
