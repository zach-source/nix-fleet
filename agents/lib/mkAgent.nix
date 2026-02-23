# mkAgent — builds an OCI image for an OpenClaw agent using nix2container
#
# The image contains Node.js 22, gh CLI, git, and CA certificates from
# nixpkgs (fully reproducible layers). OpenClaw is installed via npm on
# first container start so the image stays lean and the install can be
# cached in a Kubernetes emptyDir volume.
#
# Usage:
#   mkAgent {
#     name       = "agent-code-review";
#     configFile = ./openclaw.json;
#     soulFile   = ./SOUL.md;
#     config     = import ./config.nix;
#   }
{ n2c, pkgs }:

let
  base = import ../base { inherit pkgs; };
in

{
  name,
  configFile,
  soulFile,
  config,
}:

let
  # Files copied into the container root filesystem
  appRoot = pkgs.runCommand "agent-${name}-root" { } ''
    mkdir -p $out/home/openclaw/.openclaw/workspace
    mkdir -p $out/tmp

    cp ${configFile} $out/home/openclaw/.openclaw/openclaw.json
    cp ${soulFile}   $out/home/openclaw/.openclaw/workspace/SOUL.md
  '';

  entrypoint = pkgs.writeShellScript "agent-${name}-entrypoint" ''
    set -euo pipefail

    export HOME=/home/openclaw
    export PATH="/app/node_modules/.bin:${base.nodejs}/bin:${pkgs.gh}/bin:${pkgs.git}/bin:$PATH"

    # First-run: install openclaw (cached in emptyDir volume at /app)
    if [ ! -f /app/node_modules/.bin/openclaw ]; then
      ${base.nodejs}/bin/npm install --prefix /app openclaw@latest 2>&1
    fi

    # Authenticate gh CLI with the GitHub token from 1Password
    if [ -n "''${GITHUB_TOKEN:-}" ]; then
      echo "$GITHUB_TOKEN" | ${pkgs.gh}/bin/gh auth login --with-token 2>/dev/null || true
    fi

    # Start OpenClaw daemon
    exec /app/node_modules/.bin/openclaw start
  '';

in
n2c.buildImage {
  inherit name;
  tag = "latest";

  config = {
    entrypoint = [ "${entrypoint}" ];
    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
      "NODE_ENV=production"
      "HOME=/home/openclaw"
    ];
    WorkingDir = "/home/openclaw";
    User = "65534:65534";
  };

  copyToRoot = [ appRoot ];

  layers = [
    # Layer 1: Node.js runtime (largest, changes least)
    (n2c.buildLayer { deps = [ base.nodejs ]; })
    # Layer 2: gh CLI, git, certs, shell utilities
    (n2c.buildLayer { deps = base.systemDeps; })
  ];
}
