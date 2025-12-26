# Ubuntu Backend Compiler
# Transforms NixFleet intent modules into deployable Ubuntu artifacts
{
  pkgs,
  lib,
  config,
  ...
}:

with lib;

let
  cfg = config.nixfleet;

  # Collect failed assertions
  failedAssertions = filter (a: !a.assertion) config.assertions;

  # Build assertion error message
  assertionMessage = concatMapStringsSep "\n" (a: "- ${a.message}") failedAssertions;

  # Check assertions - this will fail evaluation if any assertions fail
  assertionsCheck =
    if failedAssertions != [ ] then
      throw ''

        NixFleet configuration failed with ${toString (length failedAssertions)} assertion(s):

        ${assertionMessage}

        Fix the above issues and try again.
      ''
    else
      true;

  # Emit warnings during evaluation
  warningsCheck =
    if config.warnings != [ ] then
      builtins.trace ''

        NixFleet configuration warnings:
        ${concatMapStringsSep "\n" (w: "- ${w}") config.warnings}
      '' true
    else
      true;

  # Force evaluation of assertions and warnings
  checksPass = assertionsCheck && warningsCheck;

  # Build the packages profile
  packagesProfile = pkgs.buildEnv {
    name = "nixfleet-packages";
    paths = cfg.packages;
    extraOutputsToInstall = [
      "man"
      "doc"
      "info"
    ];
  };

  # Generate /etc payload
  etcPayload = pkgs.runCommand "nixfleet-etc" { } ''
    mkdir -p $out

    ${concatStringsSep "\n" (
      mapAttrsToList (
        path: fileCfg:
        let
          # Determine content source
          content =
            if fileCfg.text != null then
              pkgs.writeText (baseNameOf path) fileCfg.text
            else if fileCfg.source != null then
              fileCfg.source
            else
              throw "File ${path} must have either 'text' or 'source'";

          # Create directory structure and copy file
          dir = dirOf path;
        in
        ''
          mkdir -p $out${dir}
          cp ${content} $out${path}
        ''
      ) cfg.files
    )}
  '';

  # Generate file metadata JSON for activation script
  fileMetadata = builtins.toJSON (
    mapAttrs (path: fileCfg: {
      mode = fileCfg.mode;
      owner = fileCfg.owner;
      group = fileCfg.group;
      restartUnits = fileCfg.restartUnits;
    }) cfg.files
  );

  # Generate systemd units
  unitsDir = pkgs.runCommand "nixfleet-units" { } ''
    mkdir -p $out

    ${concatStringsSep "\n" (
      mapAttrsToList (name: unitCfg: ''
        cat > $out/${name} << 'UNIT_EOF'
        ${unitCfg.text}
        UNIT_EOF
      '') cfg.systemd.units
    )}
  '';

  # Generate units metadata JSON
  unitsMetadata = builtins.toJSON (
    mapAttrs (name: unitCfg: { enabled = unitCfg.enabled; }) cfg.systemd.units
  );

  # Generate users JSON
  usersData = builtins.toJSON (
    mapAttrs (name: userCfg: {
      system = userCfg.system;
      uid = userCfg.uid;
      group = userCfg.group;
      extraGroups = userCfg.extraGroups;
      home = userCfg.home;
      createHome = userCfg.createHome;
      shell = userCfg.shell;
      description = userCfg.description;
    }) cfg.users
  );

  # Generate groups JSON
  groupsData = builtins.toJSON (
    mapAttrs (name: groupCfg: {
      gid = groupCfg.gid;
      system = groupCfg.system;
    }) cfg.groups
  );

  # Generate directories JSON
  directoriesData = builtins.toJSON (
    mapAttrs (path: dirCfg: {
      mode = dirCfg.mode;
      owner = dirCfg.owner;
      group = dirCfg.group;
    }) cfg.directories
  );

  # Generate health checks JSON
  healthChecksData = builtins.toJSON cfg.healthChecks;

  # Generate secrets payload (encrypted .age files)
  secretsPayload = pkgs.runCommand "nixfleet-secrets" { } ''
    mkdir -p $out

    ${concatStringsSep "\n" (
      mapAttrsToList (name: secretCfg: ''
        cp ${secretCfg.source} $out/${name}.age
      '') cfg.secrets.items
    )}
  '';

  # Generate secrets metadata JSON
  secretsMetadata = builtins.toJSON (
    mapAttrs (name: secretCfg: {
      path = secretCfg.path;
      owner = secretCfg.owner;
      group = secretCfg.group;
      mode = secretCfg.mode;
      restartUnits = secretCfg.restartUnits;
    }) cfg.secrets.items
  );

  # Calculate manifest hash for change detection
  manifestInputs = builtins.toJSON {
    packages = map (p: p.outPath) cfg.packages;
    files = mapAttrs (path: fileCfg: {
      text = fileCfg.text;
      source = if fileCfg.source != null then builtins.hashFile "sha256" fileCfg.source else null;
      mode = fileCfg.mode;
      owner = fileCfg.owner;
      group = fileCfg.group;
    }) cfg.files;
    units = mapAttrs (name: unitCfg: {
      text = unitCfg.text;
      enabled = unitCfg.enabled;
    }) cfg.systemd.units;
    users = cfg.users;
    groups = cfg.groups;
    directories = cfg.directories;
    secrets = mapAttrs (name: secretCfg: {
      source = builtins.hashFile "sha256" secretCfg.source;
      path = secretCfg.path;
      owner = secretCfg.owner;
      group = secretCfg.group;
      mode = secretCfg.mode;
    }) cfg.secrets.items;
    secretsMode = cfg.secrets.mode;
  };

  manifestHash = builtins.hashString "sha256" manifestInputs;

  # Main activation script
  activateScript = pkgs.writeShellScript "nixfleet-activate" ''
    set -euo pipefail

    # Ensure Nix is in PATH
    if [ -e '/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh' ]; then
      . '/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh'
    fi

    NIXFLEET_ROOT="/nix/var/nix/profiles/nixfleet"
    NIXFLEET_STATE="/var/lib/nixfleet"
    STAGING_DIR="/etc/.nixfleet/staging"
    SYSTEM_LINK="$NIXFLEET_ROOT/system"

    log() {
      echo "[nixfleet] $*"
    }

    log "Starting NixFleet activation..."
    log "Manifest hash: ${manifestHash}"

    # Step 1: Create/update the system profile
    log "Installing system profile..."
    nix-env --profile "$SYSTEM_LINK" --set ${packagesProfile}

    # Step 2: Create directories
    log "Creating directories..."
    ${concatStringsSep "\n" (
      mapAttrsToList (path: dirCfg: ''
        mkdir -p "${path}"
        chmod ${dirCfg.mode} "${path}"
        chown ${dirCfg.owner}:${dirCfg.group} "${path}"
      '') cfg.directories
    )}

    # Step 3: Create groups
    log "Managing groups..."
    ${concatStringsSep "\n" (
      mapAttrsToList (
        name: groupCfg:
        let
          gidArg = if groupCfg.gid != null then "--gid ${toString groupCfg.gid}" else "";
          systemArg = if groupCfg.system then "--system" else "";
        in
        ''
          if ! getent group "${name}" > /dev/null 2>&1; then
            log "  Creating group: ${name}"
            groupadd ${systemArg} ${gidArg} "${name}" || true
          fi
        ''
      ) cfg.groups
    )}

    # Step 4: Create users
    log "Managing users..."
    ${concatStringsSep "\n" (
      mapAttrsToList (
        name: userCfg:
        let
          uidArg = if userCfg.uid != null then "--uid ${toString userCfg.uid}" else "";
          systemArg = if userCfg.system then "--system" else "";
          homeArg = if userCfg.home != null then "--home-dir ${userCfg.home}" else "";
          createHomeArg = if userCfg.createHome then "--create-home" else "";
          shellArg = if userCfg.shell != null then "--shell ${userCfg.shell}" else "";
          groupArg = "--gid ${userCfg.group}";
          extraGroupsArg =
            if userCfg.extraGroups != [ ] then "--groups ${concatStringsSep "," userCfg.extraGroups}" else "";
          commentArg = if userCfg.description != "" then "--comment '${userCfg.description}'" else "";
        in
        ''
          if ! id "${name}" > /dev/null 2>&1; then
            log "  Creating user: ${name}"
            useradd ${systemArg} ${uidArg} ${groupArg} ${homeArg} ${createHomeArg} ${shellArg} ${extraGroupsArg} ${commentArg} "${name}" || true
          fi
        ''
      ) cfg.users
    )}

    # Step 5: Decrypt and deploy secrets
    log "Deploying secrets..."
    SECRETS_DIR="${cfg.secrets.secretsDir}"
    CHANGED_SECRETS=""

    # Create secrets directory with restrictive permissions
    mkdir -p "$SECRETS_DIR"
    chmod 0700 "$SECRETS_DIR"

    # Determine decryption identity based on mode
    SECRETS_MODE="${cfg.secrets.mode}"
    DECRYPT_READY=false

    case "$SECRETS_MODE" in
      ssh-host-key)
        SSH_HOST_KEY="${cfg.secrets.sshHostKeyPath}"
        if [ -f "$SSH_HOST_KEY" ]; then
          log "  Using SSH host key for decryption: $SSH_HOST_KEY"
          # Convert SSH key to age identity (stored in temp file, deleted after use)
          AGE_IDENTITY_FILE=$(mktemp)
          if ${pkgs.ssh-to-age}/bin/ssh-to-age -private-key -i "$SSH_HOST_KEY" > "$AGE_IDENTITY_FILE" 2>/dev/null; then
            DECRYPT_READY=true
          else
            log "  Error: Failed to convert SSH host key to age identity"
            rm -f "$AGE_IDENTITY_FILE"
          fi
        else
          log "  Error: SSH host key not found at $SSH_HOST_KEY"
        fi
        ;;
      age-key)
        AGE_KEY="${
          if cfg.secrets.ageKeyPath != null then
            cfg.secrets.ageKeyPath
          else if cfg.ageKeyPath != null then
            cfg.ageKeyPath
          else
            "/root/.config/age/key.txt"
        }"
        if [ -f "$AGE_KEY" ]; then
          log "  Using age key for decryption: $AGE_KEY"
          AGE_IDENTITY_FILE="$AGE_KEY"
          DECRYPT_READY=true
        else
          log "  Warning: Age key not found at $AGE_KEY"
        fi
        ;;
      *)
        log "  Error: Unknown secrets mode: $SECRETS_MODE"
        ;;
    esac

    if [ "$DECRYPT_READY" = "true" ]; then
      ${concatStringsSep "\n" (
        mapAttrsToList (name: secretCfg: ''
          SECRET_SRC="${secretsPayload}/${name}.age"
          SECRET_DST="${secretCfg.path}"

          if [ -f "$SECRET_SRC" ]; then
            # Create parent directory if needed
            mkdir -p "$(dirname "$SECRET_DST")"

            # Decrypt secret to temporary file first
            TEMP_SECRET=$(mktemp)
            if ${pkgs.age}/bin/age -d -i "$AGE_IDENTITY_FILE" -o "$TEMP_SECRET" "$SECRET_SRC" 2>/dev/null; then
              # Check if secret changed
              if ! cmp -s "$TEMP_SECRET" "$SECRET_DST" 2>/dev/null; then
                log "  Updating secret: ${name}"
                mv "$TEMP_SECRET" "$SECRET_DST"
                chmod ${secretCfg.mode} "$SECRET_DST"
                chown ${secretCfg.owner}:${secretCfg.group} "$SECRET_DST"
                CHANGED_SECRETS="$CHANGED_SECRETS ${name}"
              else
                rm -f "$TEMP_SECRET"
              fi
            else
              log "  Warning: Failed to decrypt secret ${name}"
              rm -f "$TEMP_SECRET"
            fi
          fi
        '') cfg.secrets.items
      )}

      # Clean up temporary identity file if we created one
      if [ "$SECRETS_MODE" = "ssh-host-key" ]; then
        rm -f "$AGE_IDENTITY_FILE"
      fi
    else
      log "  Warning: No valid decryption identity available - skipping secrets"
    fi

    # Step 6: Stage and deploy /etc files
    log "Deploying managed files..."
    CHANGED_FILES=""
    ${concatStringsSep "\n" (
      mapAttrsToList (
        path: fileCfg:
        let
          stagedPath = "${etcPayload}${path}";
        in
        ''
          if [ -f "${stagedPath}" ]; then
            # Check if file changed
            if ! cmp -s "${stagedPath}" "${path}" 2>/dev/null; then
              log "  Updating: ${path}"
              mkdir -p "$(dirname "${path}")"
              cp "${stagedPath}" "${path}"
              chmod ${fileCfg.mode} "${path}"
              chown ${fileCfg.owner}:${fileCfg.group} "${path}"
              CHANGED_FILES="$CHANGED_FILES ${path}"
            fi
          fi
        ''
      ) cfg.files
    )}

    # Step 7: Deploy systemd units
    log "Deploying systemd units..."
    CHANGED_UNITS=""
    ${concatStringsSep "\n" (
      mapAttrsToList (name: unitCfg: ''
        UNIT_SRC="${unitsDir}/${name}"
        UNIT_DST="/etc/systemd/system/${name}"

        if [ -f "$UNIT_SRC" ]; then
          if ! cmp -s "$UNIT_SRC" "$UNIT_DST" 2>/dev/null; then
            log "  Installing: ${name}"
            cp "$UNIT_SRC" "$UNIT_DST"
            chmod 0644 "$UNIT_DST"
            CHANGED_UNITS="$CHANGED_UNITS ${name}"
          fi

          ${
            if unitCfg.enabled then
              ''
                systemctl enable "${name}" 2>/dev/null || true
              ''
            else
              ''
                systemctl disable "${name}" 2>/dev/null || true
              ''
          }
        fi
      '') cfg.systemd.units
    )}

    # Step 8: Reload systemd if units changed
    if [ -n "$CHANGED_UNITS" ]; then
      log "Reloading systemd daemon..."
      systemctl daemon-reload
    fi

    # Step 9: Restart units that depend on changed files or secrets
    UNITS_TO_RESTART=""
    ${concatStringsSep "\n" (
      mapAttrsToList (
        path: fileCfg:
        concatStringsSep "\n" (
          map (unit: ''
            if echo "$CHANGED_FILES" | grep -q "${path}"; then
              if ! echo "$UNITS_TO_RESTART" | grep -q "${unit}"; then
                UNITS_TO_RESTART="$UNITS_TO_RESTART ${unit}"
              fi
            fi
          '') fileCfg.restartUnits
        )
      ) cfg.files
    )}

    # Check for units that depend on changed secrets
    ${concatStringsSep "\n" (
      mapAttrsToList (
        name: secretCfg:
        concatStringsSep "\n" (
          map (unit: ''
            if echo "$CHANGED_SECRETS" | grep -q "${name}"; then
              if ! echo "$UNITS_TO_RESTART" | grep -q "${unit}"; then
                UNITS_TO_RESTART="$UNITS_TO_RESTART ${unit}"
              fi
            fi
          '') secretCfg.restartUnits
        )
      ) cfg.secrets.items
    )}

    # Also restart changed units
    for unit in $CHANGED_UNITS; do
      if ! echo "$UNITS_TO_RESTART" | grep -q "$unit"; then
        UNITS_TO_RESTART="$UNITS_TO_RESTART $unit"
      fi
    done

    if [ -n "$UNITS_TO_RESTART" ]; then
      log "Restarting affected units:$UNITS_TO_RESTART"
      for unit in $UNITS_TO_RESTART; do
        systemctl restart "$unit" 2>/dev/null || log "  Warning: Failed to restart $unit"
      done
    fi

    # Step 10: Run post-activate hook
    ${optionalString (cfg.hooks.postActivate != "") ''
      log "Running post-activate hook..."
      ${cfg.hooks.postActivate}
    ''}

    # Step 11: Update state file
    log "Updating state..."
    mkdir -p "$NIXFLEET_STATE"
    cat > "$NIXFLEET_STATE/state.json" << 'STATE_EOF'
    {
      "generation": $(readlink "$SYSTEM_LINK" | grep -oE '[0-9]+' | tail -1 || echo 0),
      "manifestHash": "${manifestHash}",
      "lastApply": "$(date -Iseconds)",
      "activatedUnits": [${concatStringsSep "," (map (u: "\"${u}\"") (attrNames cfg.systemd.units))}],
      "managedFiles": [${concatStringsSep "," (map (f: "\"${f}\"") (attrNames cfg.files))}]
    }
    STATE_EOF

    log "Activation complete!"
  '';

in
{
  # The compiled Ubuntu system
  config.nixfleet.ubuntu = {
    # Main system derivation
    # Note: checksPass is evaluated to ensure assertions pass before building
    system =
      assert checksPass;
      pkgs.runCommand "nixfleet-ubuntu-system-${cfg.host.name}"
        {
          passthru = {
            inherit
              packagesProfile
              etcPayload
              unitsDir
              secretsPayload
              activateScript
              manifestHash
              ;
            inherit (cfg) files;
            inherit (cfg.systemd) units;
            inherit (cfg)
              users
              groups
              directories
              ;
            secrets = cfg.secrets.items;
          };
        }
        ''
          mkdir -p $out/bin

          # Link the packages profile
          ln -s ${packagesProfile} $out/packages

          # Link the etc payload
          ln -s ${etcPayload} $out/etc

          # Link the units
          ln -s ${unitsDir} $out/units

          # Link the secrets (encrypted)
          ln -s ${secretsPayload} $out/secrets

          # Install the activation script
          cp ${activateScript} $out/activate
          chmod +x $out/activate

          # Write metadata
          echo '${fileMetadata}' > $out/files.json
          echo '${unitsMetadata}' > $out/units.json
          echo '${usersData}' > $out/users.json
          echo '${groupsData}' > $out/groups.json
          echo '${directoriesData}' > $out/directories.json
          echo '${healthChecksData}' > $out/health-checks.json
          echo '${secretsMetadata}' > $out/secrets.json
          echo '${manifestHash}' > $out/manifest-hash

          # Create bin symlinks for convenience
          for bin in ${packagesProfile}/bin/*; do
            ln -s "$bin" $out/bin/ 2>/dev/null || true
          done
        '';

    # Manifest hash for change detection
    inherit manifestHash;
  };
}
