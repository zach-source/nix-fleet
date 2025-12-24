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
│       ├── apply/          # Deployment pipeline
│       ├── cache/          # Build cache management
│       ├── health/         # Health check execution
│       ├── inventory/      # Host inventory loading
│       ├── nix/            # Nix evaluation and deployment
│       ├── osupdate/       # OS update management
│       ├── preflight/      # Pre-deployment checks
│       ├── pullmode/       # GitOps pull-based deployment
│       ├── reboot/         # Reboot orchestration
│       ├── secrets/        # Age-encrypted secrets
│       ├── server/         # HTTP API server + web UI
│       ├── ssh/            # SSH client, pool, executor
│       └── state/          # Host state tracking
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

- **server/**: HTTP API at `/api/*` with embedded web UI
- **state/**: `HostState` struct with health, packages, OS info
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
