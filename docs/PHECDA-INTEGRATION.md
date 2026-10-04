# ISC Phecda Integration Boundary

This document defines the boundary between ISC-Core and ISC Phecda as of v0.2.0. It does not
make ISC-Core depend on the GUI repository: the GUI consumes the kernel through the versioned
`libisc` C ABI only.

**This document was rewritten in v0.2.0.** It previously assigned runtime downloads, dependency
installation, build processes, application lifecycle, health checks, logs, ports and recovery to
a separate Phecda Supervisor component. That split is reversed by [D25](./DECISIONS.md#d25-内核托管业务服务生命周期),
[D26](./DECISIONS.md#d26-进程模型与引擎的可分离性) and [D28](./DECISIONS.md#d28-运行时供给策略).

## Responsibilities

ISC-Core owns the entire path from a source directory to a public HTTPS site:

- **Source inspection** — read-only stack detection over a local directory.
- **Runtime provisioning** — detecting system interpreters, downloading and verifying
  prebuilt distributions, caching them locally.
- **Build** — dependency installation and build steps for the selected preset.
- **Process supervision** — start, stop, restart policy, health checks, log capture,
  local port allocation and crash recovery.
- **Public access** — DNS records and dynamic DNS tasks, reverse proxy routes, ACME
  certificates and renewal, reachability probes, firewall plans and rollback, external
  verification sessions.
- **Kernel services** — jobs, audit records and events for everything above.

ISC Phecda is a **presentation client and nothing else**:

- It renders kernel state and issues kernel commands.
- It contains no functional logic: no process management, no downloads, no build steps,
  no persistence of authoritative state.
- It must not import Go packages or internal ISC-Core types. It links the versioned
  `libisc.dylib` / `libisc.h` C ABI, and every functional call goes through
  `isc_call(method, path, body)` against paths defined in `api/openapi.yaml`.

## Server creation modes

**Presets.** The user picks a source directory. The kernel inspects it read-only — never
executing anything the source provides — and reports evidence, a recommended preset, the
runtime it requires, and how much will be downloaded. Supported website presets in v0.2.0:

- Static HTML/CSS/JavaScript
- Node.js
- Python
- PHP
- Go
- Java
- .NET
- Docker (Compose file, Dockerfile directory, existing image, simple command)

Game-server presets are not part of v0.2.0; the `gameServer` purpose field exists but has no
presets behind it.

**Custom server.** When no preset fits, the user supplies an executable and its arguments
directly. This is the only place the kernel runs a user-supplied command line; see
"Execution boundary" below for the rules that apply.

## Runtime provisioning

Resolution order, first match wins ([D28](./DECISIONS.md#d28-运行时供给策略)):

1. A system interpreter on `PATH` that satisfies the preset's minimum version.
2. A runtime already provisioned under the kernel's data directory.
3. A prebuilt distribution downloaded from a pinned URL and verified against a pinned
   SHA-256.

PHP and Python are obtained from established third-party prebuilt distributions because
neither language publishes official macOS binaries. Go, Java and .NET are fetched on demand
with their size shown before the download begins.

**One digest is not an upstream-published checksum.** Node, Go, Python, Temurin and the .NET
SDK each ship a checksum file or release metadata the pinned value was taken from
(the provenance is recorded per entry in the manifest's `Verified` field). PHP has no such
thing on macOS: the only viable source publishes no checksums at all. Its digest was
computed locally from the official channel on first download — trust-on-first-use. That is
recorded honestly rather than dressed up as an upstream checksum, and it still buys
something real: every later provision must produce byte-identical content or it fails
loudly instead of silently installing something else.

### Re-verifying a pinned entry

Two things about a manifest entry can rot without any test failing: the URL stops serving, and
the archive's *layout* changes (so `Executable` points at nothing). Both are cheap to check
without pulling hundreds of megabytes:

```bash
# 1. Header only: does the URL still serve, and what do the first entries look like?
curl -sSL -r 0-200000 <url> -o head.tar.gz
tar -tzf head.tar.gz | head -20          # truncated gzip; the error at the end is expected

# 2. Full pull, digest check (what the kernel itself does)
curl -sSL <url> -o full.tar.gz && shasum -a 256 full.tar.gz
```

Verified on 2026-10-04 by that method: Go wraps everything in `go/` and .NET SDK has **no**
wrapper directory (its first entry is `./dotnet`), Temurin wraps in `jdk-21.0.12.1+1/` with the
JDK under `Contents/Home/`. Python and PHP were verified end to end instead — Python was
downloaded, verified and deployed, PHP re-downloaded byte-identical to its pinned digest.

A runtime is only treated as "installed by the kernel" when the directory carries a marker
file naming its kind, version and digest. A directory the user placed there by hand is
neither used nor deleted.

Every redistributed runtime is registered in `THIRD_PARTY_NOTICES.md`
([D29](./DECISIONS.md#d29-再分发运行时的许可登记)). Downloaded runtimes are untrusted
content: HTTPS only, checksum verified while streaming, path-traversal and symlink-escape
rejected during extraction, staged and atomically renamed into place.

## Public binding

A deployed application exposes one or more domains. The public side is materialized through
the kernel's existing DDNS, proxy, certificate and verification APIs; the binding record
itself is persisted by the kernel, so a deployment's reference to it always resolves.

A DNS record, a proxy route or a certificate is not itself an application. Failure to
complete the public side does not stop the application from running locally: it stays
`running`, the public part is reported as pending, and the advisory engine explains why.

## Execution boundary

Presets run **fixed `argv` arrays defined in the kernel**, never through a shell
([D30](./DECISIONS.md#d30-业务进程的执行与隔离边界)). The custom-server path accepts an
executable and argument array, also without a shell, and requires explicit confirmation in
the GUI.

Child processes receive a **minimal environment** — an explicit allowlist plus variables the
user declared for that application — and do not inherit the kernel's environment, so the
kernel's access token and secrets cannot leak into a hosted site.

This is a **trusted local client** boundary, not a sandbox: applications run as the same user
who runs the kernel, which is equivalent to that user typing the command themselves.
Untrusted third-party code must not be hosted this way. (The existing shell-based "command"
IP source in the DDNS engine predates this rule and is a known inconsistency.)

## API surface (v2)

The v2 contract is defined in `api/openapi.yaml`, which remains the single source of truth
([D07](./DECISIONS.md#d07-api-契约形式)). Functionality added in v0.2.0:

- `/v1/presets` — preset catalog, runtime requirements and size estimates
- `/v1/sources/inspect` — read-only detection over a directory
- `/v1/runtimes`, `/v1/runtimes/provision`, `/v1/runtimes/{kind}` — runtime inventory and provisioning
- `/v1/apps`, `/v1/apps/{id}`, `/v1/apps/{id}/deploy|start|stop|restart`, `/v1/apps/{id}/logs` — application lifecycle
- `/v1/metrics` — host and per-application resource samples
- `/v1/advisories` — actionable suggestions derived from current state

The v1 Phecda endpoints (`/v1/phecda/*` and `/v1/public-services`) were **removed**, not
deprecated; the GUI is the only consumer and no Phecda release had shipped against them. The
interface version was therefore bumped to `v2` ([D27](./DECISIONS.md#d27-契约破坏性变更与接口版本-v2)).
The nine C functions are unchanged — the library is a generic dispatcher, so new endpoints
never require touching it.

Old SQLite tables from the v1 Phecda work are retained rather than dropped: published
migration files are immutable, and deleting user data has no upside. They are simply no
longer served.

Long-running operations use the existing job and event semantics: submit a job, report
progress through `job.progress`, observe completion through `job.finished` and
`app.state_changed`. Application logs are **pulled** from `/v1/apps/{id}/logs` rather than
pushed as events, because the event ring buffer is small and slow subscribers are
disconnected — log volume would evict real state changes.

Secrets remain references to the platform secret store and never appear in exports, scan
results or logs.

## Containerized sites

A `docker-compose` project is recognisable and runnable: the kernel runs `docker compose up`
in the foreground so container logs land in the application log, and health-checks the port
the user's compose file is expected to publish. Docker itself is **never** provisioned —
Docker Desktop has to be installed by the user, and a deploy attempt without it fails
immediately with that reason rather than with a `command not found` from deep inside the
build.

There is deliberately no "run an arbitrary image" preset. It needs an input the kernel
cannot guess (the image name and the container port), so it would only be a wizard entry
that fails afterwards; the custom-server path covers it with an explicit command.

## Product boundaries

ISC Mizar is a future remote monitoring client and requires a separate authenticated
remote-management service. ISC Dubhe is a future cluster control plane and requires agents,
node identity, scheduling and multi-node state. Neither product is enabled by this local API
boundary.

The kernel also does not install Docker Desktop, does not manage container internals beyond
invoking the `docker` CLI, and (per [D26](./DECISIONS.md#d26-进程模型与引擎的可分离性)) does not
install itself as a system service in v0.2.0.
