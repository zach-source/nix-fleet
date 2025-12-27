# NixFleet - Claude Code Instructions

## Project Overview

NixFleet is a fleet management CLI tool for deploying Nix configurations to non-NixOS hosts (Ubuntu, macOS). It manages packages, files, users, systemd units, and health checks via SSH.

## Version Control: Use jj (Jujutsu)

This repo uses **jj** (Jujutsu) instead of git commands. Always use jj:

```bash
# Status and log
jj status
jj log --limit 10

# Describe current change (like git commit -m)
jj describe -m "Your commit message"

# Create new empty change on top (finalizes current change)
jj new

# Push to remote
jj git push

# Fetch from remote
jj git fetch

# Common workflow: describe, new, push
jj describe -m "Add feature X" && jj new && jj git push
```

### jj Tips
- Working copy is always a change in progress - use `jj describe` to set message
- `jj new` creates a new empty change, effectively "committing" current work
- Bookmarks are like git branches: `jj bookmark set main -r @-`
- If bookmark tracking issues: `jj bookmark track main@origin`

## Project Structure

```
nixfleet/
├── cmd/nixfleet/           # Main CLI application
│   ├── main.go             # Entry point with Cobra commands
│   └── internal/           # Internal packages
│       ├── apt/            # APT package management for Ubuntu
│       ├── apply/          # Deployment pipeline with reconciliation
│       ├── cache/          # Build cache management
│       ├── health/         # Health check execution
│       ├── inventory/      # Host inventory loading
│       ├── k0s/            # k0s reconciliation and status
│       ├── nix/            # Nix evaluation and deployment
│       ├── osupdate/       # OS update management
│       ├── pki/            # Fleet PKI (CA, certificates)
│       ├── preflight/      # Pre-deployment checks
│       ├── pullmode/       # GitOps pull-based deployment
│       ├── reboot/         # Reboot orchestration
│       ├── secrets/        # Age-encrypted secrets
│       ├── server/         # HTTP API server + web UI
│       ├── ssh/            # SSH client, pool, executor
│       └── state/          # Host state tracking (includes K0sState)
├── flake.nix               # Nix flake for building
├── backends/               # Backend-specific code
├── hosts/                  # Example host configurations
├── modules/                # NixFleet Nix modules
├── lib/                    # Nix library functions
└── secrets/                # Encrypted secrets
```

## Development

### Build and Run
```bash
# Enter dev shell
nix develop

# Build
go build -o nixfleet ./cmd/nixfleet

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

```
GET  /api/hosts                      # List all hosts
GET  /api/hosts/{name}               # Get host details
GET  /api/hosts/{name}/os-info       # Get OS information
GET  /api/hosts/{name}/apt/updates   # Check for APT updates
POST /api/hosts/{name}/apt/upgrade   # Run apt upgrade
POST /api/hosts/{name}/apt/install   # Install package
POST /api/hosts/{name}/apt/remove    # Remove package
POST /api/hosts/{name}/apt/autoremove # Run apt autoremove
POST /api/hosts/{name}/apt/clean     # Clean apt cache
POST /api/hosts/{name}/deploy        # Trigger deployment
```

## Testing

```bash
go test ./cmd/nixfleet/internal/...
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

1. **Update version** in `flake.nix` if needed

2. **Create and push tag**:
   ```bash
   jj describe -m "Release v0.x.x" && jj new
   jj bookmark set main -r @-
   jj git push

   # Create tag (use git for tags)
   git tag v0.x.x
   git push origin v0.x.x
   ```

3. **GitHub Actions will automatically**:
   - Build binaries for linux/darwin (amd64/arm64)
   - Create GitHub release with tarballs and checksums
   - Trigger homebrew-tap update

4. **Manual steps after release**:
   - Update `nix-packages` overlay with new version hash
   - Verify homebrew formula updated correctly

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
| Nix | `zach-source/nix-packages` | Manual overlay update |
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
| `TAP_GITHUB_TOKEN` | PAT for triggering homebrew-tap update |
