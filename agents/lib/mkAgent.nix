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
    outputHash = "sha256-HmsQSi/u4R+SRWuviZe+lAuaQMskq+PE3o4GANlCCFw=";
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

    # Many npm packages use #!/usr/bin/env in shebangs
    ln -s ${pkgs.coreutils}/bin/env $out/usr/bin/env
  '';

  # Generate plugin enable commands from the plugins list
  pluginEnableCommands = builtins.concatStringsSep "\n" (
    builtins.map (p: ''
      ${openclawApp}/node_modules/.bin/openclaw plugins enable ${p} 2>/dev/null || true
    '') plugins
  );

  entrypoint = pkgs.writeShellScript "agent-${name}-entrypoint" ''
    set -euo pipefail

    export HOME=/home/agent
    export PATH="${openclawApp}/node_modules/.bin:${base.nodejs}/bin:${pkgs.gh}/bin:${pkgs.git}/bin:$PATH"

    # Copy read-only configs to writable HOME
    ${pkgs.coreutils}/bin/mkdir -p /home/agent/.openclaw/workspace
    ${pkgs.coreutils}/bin/cp /etc/openclaw/openclaw.json /home/agent/.openclaw/openclaw.json
    ${pkgs.coreutils}/bin/cp /etc/openclaw/workspace/SOUL.md /home/agent/.openclaw/workspace/SOUL.md

    # Enable channel plugins (writes to ~/.openclaw/openclaw.json)
    ${pluginEnableCommands}

    # Authenticate gh CLI with the GitHub token from 1Password
    if [ -n "''${GITHUB_TOKEN:-}" ]; then
      echo "$GITHUB_TOKEN" | ${pkgs.gh}/bin/gh auth login --with-token 2>/dev/null || true
    fi

    # Start OpenClaw gateway
    exec ${openclawApp}/node_modules/.bin/openclaw gateway
  '';

in
n2c.buildImage {
  inherit name;
  tag = "latest";

  config = {
    entrypoint = [ "${entrypoint}" ];
    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
      "LD_LIBRARY_PATH=${pkgs.glibc}/lib"
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
