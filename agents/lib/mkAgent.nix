# mkAgent — builds an OCI image for an OpenClaw agent using nix2container
#
# The image contains Node.js 22, gh CLI, git, CA certificates, and OpenClaw
# pre-installed from npm. Config files are baked in at /etc/openclaw and
# copied to the writable HOME at startup.
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
  plugins ? [ ],
}:

let
  # Install OpenClaw via npm at build time (FOD — fixed-output derivation)
  openclawApp = pkgs.stdenv.mkDerivation {
    pname = "openclaw-app";
    version = "latest";

    # No source — we just run npm install
    dontUnpack = true;

    nativeBuildInputs = [
      base.nodejs
      pkgs.cacert
      pkgs.git
    ];

    buildPhase = ''
      export HOME=$TMPDIR
      export SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt
      mkdir -p $TMPDIR/app
      cd $TMPDIR/app
      ${base.nodejs}/bin/npm init -y > /dev/null 2>&1
      ${base.nodejs}/bin/npm install openclaw@latest --omit=dev --ignore-scripts 2>&1
      mkdir -p $out
      cp -a $TMPDIR/app/. $out/
    '';

    dontInstall = true;
    dontPatchShebangs = true;
    dontFixup = true;

    # npm needs network access
    outputHashAlgo = "sha256";
    outputHashMode = "recursive";
    outputHash = "sha256-axCeUyP5rrVmhxkY/lW4TH7X69mBud8WzpVYOIAL460=";
  };

  # Config files baked into the image at /etc/openclaw (read-only)
  appRoot = pkgs.runCommand "agent-${name}-root" { } ''
    mkdir -p $out/etc/openclaw/workspace
    mkdir -p $out/tmp
    mkdir -p $out/usr/bin

    cp ${configFile} $out/etc/openclaw/openclaw.json
    cp ${soulFile}   $out/etc/openclaw/workspace/SOUL.md

    # glibc getaddrinfo needs nsswitch.conf for DNS resolution
    echo "hosts: files dns" > $out/etc/nsswitch.conf

    # Patch dns.lookup to use c-ares with retry (handles intermittent CoreDNS failures)
    cp ${./dns-patch.js} $out/etc/openclaw/dns-patch.js

    # Exec approvals for headless K8s — auto-approve all exec, no interactive prompts
    echo '{"version":1,"defaults":{"security":"full","ask":"off","autoAllowSkills":true},"agents":{}}' > $out/etc/openclaw/exec-approvals.json

    # GitHub App token generator (PEM → JWT → installation token)
    cp ${./gh-app-token.sh} $out/etc/openclaw/gh-app-token.sh

    # Many npm packages use #!/usr/bin/env in shebangs
    ln -s ${pkgs.coreutils}/bin/env $out/usr/bin/env

    # OpenClaw exec tool spawns "sh" — needs /bin/sh to exist
    mkdir -p $out/bin
    ln -s ${pkgs.bashInteractive}/bin/bash $out/bin/sh
    ln -s ${pkgs.bashInteractive}/bin/bash $out/bin/bash
  '';

  # Generate plugin enable commands from the plugins list
  pluginEnableCommands = builtins.concatStringsSep "\n" (
    builtins.map (p: ''
      ${openclawApp}/node_modules/.bin/openclaw plugins enable ${p} 2>/dev/null || true
    '') plugins
  );

  entrypoint = pkgs.writeTextFile {
    name = "agent-${name}-entrypoint";
    executable = true;
    text = ''
      #!${pkgs.bashInteractive}/bin/bash
      set -euo pipefail

      export HOME=/home/agent
      export PATH="${openclawApp}/node_modules/.bin:${base.nodejs}/bin:${pkgs.gh}/bin:${pkgs.git}/bin:${pkgs.bashInteractive}/bin:${pkgs.coreutils}/bin:${pkgs.jq}/bin:${pkgs.curl}/bin:${pkgs.gnugrep}/bin:${pkgs.findutils}/bin:${pkgs.gnused}/bin:${pkgs.gawk}/bin:${pkgs.openssl}/bin:$PATH"

      # Copy read-only configs to writable HOME
      ${pkgs.coreutils}/bin/mkdir -p /home/agent/.openclaw/workspace
      ${pkgs.coreutils}/bin/cp /etc/openclaw/openclaw.json /home/agent/.openclaw/openclaw.json
      ${pkgs.coreutils}/bin/cp /etc/openclaw/workspace/SOUL.md /home/agent/.openclaw/workspace/SOUL.md

      # Seed cron jobs (always overwrite to pick up config changes)
      if [ -f /etc/openclaw/cron-jobs.json ]; then
        ${pkgs.coreutils}/bin/mkdir -p /home/agent/.openclaw/cron
        ${pkgs.coreutils}/bin/cp /etc/openclaw/cron-jobs.json /home/agent/.openclaw/cron/jobs.json
      fi

      # Exec approvals: headless K8s — security=full, ask=off (no interactive prompts)
      # Only seed if missing so agent-customized approvals on NFS are preserved
      if [ ! -f /home/agent/.openclaw/exec-approvals.json ]; then
        ${pkgs.coreutils}/bin/cp /etc/openclaw/exec-approvals.json /home/agent/.openclaw/exec-approvals.json
      fi

      # Enable channel plugins (writes to ~/.openclaw/openclaw.json)
      ${pluginEnableCommands}

      # GitHub authentication — supports GitHub App (preferred) or static PAT (legacy)
      if [ -n "''${GITHUB_APP_ID:-}" ] && [ -n "''${GITHUB_APP_PRIVATE_KEY_B64:-}" ] && [ -n "''${GITHUB_APP_INSTALLATION_ID:-}" ]; then
        # GitHub App: generate short-lived installation token (1hr)
        ${pkgs.coreutils}/bin/cp /etc/openclaw/gh-app-token.sh /home/agent/gh-app-token.sh
        ${pkgs.coreutils}/bin/chmod +x /home/agent/gh-app-token.sh
        GITHUB_TOKEN=$(/home/agent/gh-app-token.sh)
        export GITHUB_TOKEN
        echo "$GITHUB_TOKEN" | ${pkgs.gh}/bin/gh auth login --with-token 2>/dev/null || true
        echo "$GITHUB_TOKEN" > /home/agent/.github-token

        # Background refresh: generate new token every 50 minutes
        (
          while true; do
            sleep 3000
            NEW_TOKEN=$(/home/agent/gh-app-token.sh 2>/dev/null) || continue
            echo "$NEW_TOKEN" > /home/agent/.github-token
            echo "$NEW_TOKEN" | ${pkgs.gh}/bin/gh auth login --with-token 2>/dev/null || true
          done
        ) &
      elif [ -n "''${GITHUB_TOKEN:-}" ]; then
        # Legacy: static PAT from 1Password
        echo "$GITHUB_TOKEN" | ${pkgs.gh}/bin/gh auth login --with-token 2>/dev/null || true
      fi

      # Start OpenClaw gateway (--verbose for structured logging → Vector → Loki)
      exec ${openclawApp}/node_modules/.bin/openclaw gateway --verbose
    '';
  };

in
n2c.buildImage {
  inherit name;
  tag = "latest";

  config = {
    entrypoint = [ "${entrypoint}" ];
    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
      "NODE_OPTIONS=--max-old-space-size=1536 --require /etc/openclaw/dns-patch.js"
      "UV_THREADPOOL_SIZE=16"
      "NODE_ENV=production"
      "HOME=/home/agent"
    ];
    WorkingDir = "/home/agent";
    User = "65534:65534";
  };

  copyToRoot = [ appRoot ];

  layers = [
    # Layer 1: Node.js runtime (largest, changes least)
    (n2c.buildLayer { deps = [ base.nodejs ]; })
    # Layer 2: gh CLI, git, certs, shell utilities
    (n2c.buildLayer { deps = base.systemDeps; })
    # Layer 3: OpenClaw application (npm install output)
    (n2c.buildLayer { deps = [ openclawApp ]; })
  ];
}
