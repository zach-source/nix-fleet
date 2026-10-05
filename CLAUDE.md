# NixFleet - Claude Code Instructions

## Project Overview

NixFleet is a fleet management CLI tool for deploying Nix configurations to non-NixOS hosts (Ubuntu, macOS). It manages packages, files, users, systemd units, and health checks via SSH.

## Version Control: Use git

This repo uses **git**. Standard workflow:

```bash
# Status and log
git status
git log --oneline -10

# Stage and commit
git add -A
git commit -m "Add feature X"

# Push to remote
git push

# Fetch / pull from remote
git fetch
git pull
```

### Tips
- Don't commit on `main` directly for larger work — branch first (`git switch -c feature/x`), then open a PR.
- Tags use git directly: `git tag v0.x.x && git push origin v0.x.x`.

## Project Structure

```
nixfleet/
├── cmd/nixfleet/           # Main CLI application
│   ├── main.go             # Entry point with Cobra commands
│   └── internal/           # Internal packages
│       ├── agenttui/       # Terminal UI for the fleet agents
│       ├── apply/          # Deployment pipeline with reconciliation
│       ├── apt/            # APT package management for Ubuntu
│       ├── cache/          # Build cache management
│       ├── health/         # Health check execution
│       ├── inventory/      # Host inventory loading
│       ├── juicefs/        # Shared JuiceFS filesystem bootstrap
│       ├── k0s/            # k0s reconciliation and status
│       ├── nix/            # Nix evaluation and deployment
│       ├── nodestatus/     # Node status HTTP server (pull mode)
│       ├── osupdate/       # OS update management
│       ├── pki/            # Fleet PKI (CA, certificates)
│       ├── preflight/      # Pre-deployment checks
│       ├── pullmode/       # GitOps pull-based deployment
│       ├── reboot/         # Reboot orchestration
│       ├── secrets/        # Age-encrypted secrets
│       ├── server/         # HTTP API server + web UI
│       ├── spire/          # SPIRE workload identity
│       ├── ssh/            # SSH client, pool, executor
│       ├── state/          # Host state tracking (includes K0sState)
│       └── synology/       # Synology DSM API backend (Model B)
├── flake.nix               # Nix flake for building
├── backends/               # Backend-specific code
├── hosts/                  # Fleet host configurations
├── modules/                # NixFleet Nix modules
├── lib/                    # Nix library functions
└── secrets/                # Encrypted secrets
```

## Development

### Build and Run
```bash
# Enter dev shell
nix develop

# Format Nix, from the repo root. The tree is nixfmt-formatted; nixpkgs-fmt
# would churn every file.
nixfmt flake.nix modules/*.nix hosts/*.nix

# The Go module is rooted at cmd/nixfleet, not the repo root — every go
# command has to run from there or it fails with "go.mod file not found".
cd cmd/nixfleet

# Build
go build -o nixfleet .

# Run tests
go test ./...

# Format Go code
go fmt ./...
```

### Key Packages

- **apply/**: Deployment pipeline with preflight, deploy, PKI, k0s reconciliation, health checks
- **k0s/**: k0s cluster management - status, reconciliation, orphan cleanup
- **pki/**: Fleet PKI with ECDSA P-256 - CA init, cert issuance, age-encrypted storage
- **state/**: Host state tracking including `K0sState` for resource tracking
- **server/**: HTTP API at `/api/*` with embedded web UI
- **apt/**: APT operations via SSH (CheckUpdates, Install, Remove, etc.)
- **nix/deployer.go**: Core deployment logic via SSH
- **pullmode/**: GitOps-style pull deployments

### Web UI

Embedded in `internal/server/ui/`:
- `index.html` - Single page dashboard
- `app.js` - Vanilla JS application
- `style.css` - Styling

Format JS with: `npx prettier --write cmd/nixfleet/internal/server/ui/app.js`

## API Endpoints

Registered in `NewServer()` in `server/server.go`. Everything except
`/api/health` and `/api/info` goes through `authMiddleware`.

```
GET  /api/health                      # Liveness (unauthenticated)
GET  /api/info                        # Version/build info (unauthenticated)

GET  /api/hosts                       # List all hosts
GET  /api/hosts/{name}                # Get host details
GET  /api/hosts/{name}/state          # Get recorded host state
POST /api/hosts/{name}/apply          # Apply configuration to one host
POST /api/hosts/{name}/rollback       # Roll one host back a generation

GET  /api/plan                        # Plan for the whole fleet
GET  /api/plan/{name}                 # Plan for one host
POST /api/apply                       # Apply to the whole fleet

GET  /api/drift                       # Drift status
POST /api/drift/check                 # Re-check drift (?host=)
POST /api/drift/fix                   # Fix drift (?host=)

GET  /api/jobs                        # List async jobs
GET  /api/jobs/{id}                   # Get one job

GET  /api/hosts/{name}/os-info        # Get OS information
GET  /api/hosts/{name}/apt/packages   # List installed APT packages
GET  /api/hosts/{name}/apt/updates    # Check for APT updates
POST /api/hosts/{name}/apt/update     # Run apt update
POST /api/hosts/{name}/apt/upgrade    # Run apt upgrade
POST /api/hosts/{name}/apt/install    # Install package
POST /api/hosts/{name}/apt/remove     # Remove package
POST /api/hosts/{name}/apt/autoremove # Run apt autoremove
POST /api/hosts/{name}/apt/clean      # Clean apt cache

GET  /api/pull-mode/status            # Pull mode status
POST /api/pull-mode/{name}/trigger    # Trigger an immediate pull

GET  /ui/                             # Embedded web UI
```

There is no `/api/hosts/{name}/deploy` — applying to one host is
`POST /api/hosts/{name}/apply`.

## Testing

The Go module is rooted at `cmd/nixfleet`, so these must be run from there:

```bash
cd cmd/nixfleet
go test ./...
```

Mock SSH client available in `ssh/mock.go` for testing.

## Nix Integration

Host configs are evaluated with:
```bash
nix eval .#nixfleetConfigurations.{hostname} --json
```

The evaluator extracts: packages, files, users, groups, directories, systemd units, health checks, hooks.

## Common Tasks

### Adding a new API endpoint
1. Add handler in `server/server.go`
2. Register route in `NewServer()`
3. Update UI in `ui/app.js` if needed

### Adding new host state
1. Add field to `HostState` or create new struct in `state/state.go`
2. Add gathering method (e.g., `GatherOSInfo()`)
3. Call from `UpdateAllHosts()` or create new update method

## Versioning & Releases

NixFleet uses [Semantic Versioning](https://semver.org/):
- **MAJOR**: Breaking changes to CLI or configuration format
- **MINOR**: New features, backward compatible
- **PATCH**: Bug fixes, backward compatible

### Current Version
- **v0.1.5** - k0s & PKI release (2025-12-26)

### Changelog
- **v0.1.5**: k0s Kubernetes support, Fleet PKI, Gateway API, resource reconciliation
- **v0.1.4**: NixOS support, Darwin improvements
- **v0.1.3**: Pull mode enhancements
- **v0.1.2**: Web UI improvements
- **v0.1.1**: Bug fixes
- **v0.1.0**: Initial release (2024-12-24)

### Creating a Release

1. **Update the version in all three places it is written** — they have no way
   of checking each other, and they have drifted before.
   `TestVersionIsConsistent` in `cmd/nixfleet/version_test.go` enforces that
   they agree:
   - `pkgs/nixfleet/default.nix` — `version = "0.x.x";`
   - `flake.nix` — the `gitTag` passed to `callPackage ./pkgs/nixfleet`
   - `CLAUDE.md` — the **Current Version** line below

2. **Create and push tag**:
   ```bash
   git commit -am "Release v0.x.x"
   git push origin main

   # Create tag
   git tag v0.x.x
   git push origin v0.x.x
   ```

3. **GitHub Actions will automatically**:
   - Build binaries for linux/darwin (amd64/arm64)
   - Create GitHub release with tarballs and checksums
   - Dispatch to both `homebrew-tap` and `nix-packages` with the SHA256s

4. **Manual steps after release**:
   - Verify the homebrew formula and the nix-packages overlay both updated

### Release Artifacts

Each release includes:
- `nixfleet-linux-amd64.tar.gz`
- `nixfleet-linux-arm64.tar.gz`
- `nixfleet-darwin-amd64.tar.gz`
- `nixfleet-darwin-arm64.tar.gz`
- `checksums.txt` (SHA256)

### Distribution Channels

| Channel | Repository | Update Method |
|---------|------------|---------------|
| Homebrew | `zach-source/homebrew-tap` | Auto via GitHub Actions |
| Nix | `zach-source/nix-packages` | Auto via GitHub Actions (`repository-dispatch`) |
| GitHub | This repo releases | Auto via GitHub Actions |

## Secrets Management

### Architecture

NixFleet uses age encryption with SSH host key integration:

```
┌──────────────────────────────────────────────────────────────┐
│                     Encryption (secrets.nix)                  │
│  Admin Keys + Host Keys (SSH-derived) → Multi-Recipient .age │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                     Decryption (on host)                      │
│  SSH Host Key → ssh-to-age → Age Identity → Plaintext        │
└──────────────────────────────────────────────────────────────┘
```

### Key Files

| File | Purpose |
|------|---------|
| `secrets/secrets.nix` | Declarative access control (keys + secret→key mapping) |
| `secrets/*.age` | Encrypted secret files |
| `~/.config/age/admin-key.txt` | Admin key for local decryption/rekey |

### CLI Commands

```bash
# Onboard new host (get age key, setup secrets)
nixfleet host onboard -H newhost --repo git@github.com:org/config.git

# Re-encrypt after modifying secrets.nix
nixfleet secrets rekey

# Edit a secret in place
nixfleet secrets edit secrets/api-key.age

# Add a new secret
echo "value" | nixfleet secrets add secret-name --host hostname
```

### Accessing 1Password Connect from bare-metal hosts

K8s pods pull secrets via the 1P Operator (OnePasswordItem CRDs). Bare-metal hosts (gtr-150..153, mac-1, docker on mac) use the in-cluster Connect server via a Cloudflare tunnel + nginx header-remap proxy (`op-proxy`).

**Why the proxy exists**: Cloudflare Access strips the `Authorization` header, but 1P Connect requires it. `op-proxy` remaps a custom `X-OP-Token` header into `Authorization: Bearer` inside the cluster.

**Flow**:
```
Host
  │  curl with: X-OP-Token, CF-Access-Client-Id, CF-Access-Client-Secret
  ▼
Cloudflare Tunnel (op.nixfleet.private.stigen.ai) — CF Access service-token enforced
  ▼
op-proxy (onepassword/op-proxy:8090) — rewrites X-OP-Token → Authorization: Bearer
  ▼
onepassword-connect (onepassword/onepassword-connect:8080)
```

**Three secrets needed per host** (in 1Password vault `Personal Agents`):

| Item | Field | Use |
|------|-------|-----|
| `op-connect-token` | `credential` | 1P Connect bearer |
| `op-connect-cf-access` | `client_id` | CF Access service token ID |
| `op-connect-cf-access` | `client_secret` | CF Access service token secret |

**Important**: the `op` CLI has no native support for custom HTTP headers, so it cannot talk to this proxy directly. Bare-metal clients use `curl` against the Connect REST API, or a library like autoeng's `OnePasswordConnectProvider`.

**Reference client**: `mcp-auto-engineering/src/shared/secret-providers.ts` → `OnePasswordConnectProvider` class. Bootstrap flow: resolve the three secrets once via `op` CLI with biometric auth, then export as env vars for downstream tools.

**Manifests**:
- Proxy: `nix-fleet-hosts/flux/apps/overlays/nixfleet/onepassword-proxy/proxy.yaml`
- Tunnel: `nix-fleet-hosts/flux/apps/overlays/nixfleet/cloudflare-tunnel/configmap.yaml`

## CI/CD

### Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `ci.yml` | Push/PR to main | Build, test, lint, nix check |
| `release.yml` | Tag push (v*) | Build releases, create GitHub release |

### Required Secrets

| Secret | Purpose |
|--------|---------|
| `GITHUB_TOKEN` | Auto-provided, for releases |
| `PACKAGES_TOKEN` | PAT for dispatching to `homebrew-tap` and `nix-packages` |
