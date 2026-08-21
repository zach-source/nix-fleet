# NixFleet DeepSeek-V4-Flash module (dspark recipe, Docker Compose)
#
# Runs DeepSeek-V4-Flash TP=2 across a stacked DGX Spark pair using the MiaAI-Lab
# recipe. This replaces the inert `nixfleet.modules.vllm` declaration that used to
# describe the same workload: that one assumed a native vLLM entrypoint at
# /opt/dsv4, which does not exist and never did, so it shipped disabled and the
# model was started by hand instead.
#
# Why the vendor's own scripts rather than a unit that calls `docker compose`
# directly: start-deepseek-v4-flash-dspark.sh resolves the RoCE GID index by
# matching an IPv4 against the HCA's GID table on BOTH nodes, then exports the
# result into the worker's environment over SSH. Getting that wrong doesn't fail
# loudly, it makes NCCL fall back or hang in rendezvous. Reimplementing 741 lines
# of that in Nix buys nothing, so the module owns everything AROUND the scripts:
# which image, which recipe commit, which settings, and the lifecycle.
#
# Topology: rank 0 is the head. It serves HTTP and starts the worker over SSH,
# so only rank 0 gets a service unit. Rank 1 still needs the checkout and the
# env file, because the head's SSH command runs `docker compose --env-file
# .env.dspark` in the worker's own directory.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.dsparkDsv4;

  isHead = cfg.nodeRank == 0;

  # `image` is an option in its own right rather than just another settings key,
  # so it gets merged in here. Nix iterates attrsets in key order, so the
  # rendered file is stable across unrelated edits and won't re-trigger the
  # restart below for no reason.
  allSettings = cfg.settings // {
    DSPARK_VLLM_IMAGE = cfg.image;
  };

  envText = lib.concatStringsSep "\n" (
    lib.mapAttrsToList (k: v: "${k}=${v}") (lib.filterAttrs (_: v: v != null) allSettings)
  );

  startScript = "${cfg.recipeDir}/start-deepseek-v4-flash-dspark.sh";
  stopScript = "${cfg.recipeDir}/stop-deepseek-v4-flash-dspark.sh";
in
{
  options.nixfleet.modules.dsparkDsv4 = {
    enable = lib.mkEnableOption "DeepSeek-V4-Flash via the dspark Docker Compose recipe";

    nodeRank = lib.mkOption {
      type = lib.types.int;
      description = ''
        0 = head (serves HTTP, starts the worker over SSH), 1 = worker.
        Must match NODE_RANK in `settings` — they are separate because the
        recipe reads the env file directly and this module reads the rank to
        decide whether to install a service unit at all.
      '';
      example = 0;
    };

    image = lib.mkOption {
      type = lib.types.str;
      default = "ghcr.io/zach-source/dspark-vllm-gx10:0.1.1@sha256:a83948492cf13df455170fb42885f5ef4db54fefe0feff0f841ecbff464ac9d8";
      description = ''
        Runtime image, digest-pinned. Defaults to our own GHCR mirror rather
        than ghcr.io/anemll/... so an upstream delete or retag can't change what
        the fleet runs — the digest is byte-identical to upstream 0.1.1.

        The mirror is PRIVATE, because the image bundles CUDA, PyTorch and
        Triton under their own upstream terms; re-hosting those publicly isn't
        ours to do. A cold pull therefore needs a read:packages credential in
        the host's docker config. That is not wired up: both Sparks already have
        the image resident and the compose file sets `pull_policy:
        if_not_present`, so nothing pulls today. Re-imaging a Spark needs the
        credential first.
      '';
    };

    recipeUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://github.com/MiaAI-Lab/DeepSeek-v4-Flash-DSpark-2x-DGX-Spark";
      description = "Upstream recipe repository.";
    };

    recipeRev = lib.mkOption {
      type = lib.types.str;
      default = "5d5a00d";
      description = ''
        Recipe commit to check out. Pinned rather than tracking a branch: the
        compose file, the entrypoint and roughly a dozen hotfix scripts all ship
        from here and are mounted into the container, so "whatever main is
        today" would silently change the runtime on the next deploy.
      '';
    };

    recipeDir = lib.mkOption {
      type = lib.types.str;
      default = "/opt/dspark";
      description = ''
        Checkout location. Must be identical on both nodes — the head passes its
        own WORKER_DIR to the worker over SSH.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "deploy";
      description = ''
        User that owns the checkout and runs the stack. Needs membership in
        `docker`, which this module declares.
      '';
    };

    settings = lib.mkOption {
      type = lib.types.attrsOf (lib.types.nullOr lib.types.str);
      default = { };
      description = ''
        Contents of the recipe's env file, rendered one KEY=value per line.
        Set a key to null to omit it. See .env.dspark.example in the recipe for
        the full surface (68 keys).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # git for the checkout below; the recipe's scripts are bash + docker, both
    # already present on DGX OS.
    nixfleet.packages = with pkgs; [ git ];

    nixfleet.users.${cfg.user}.extraGroups = [ "docker" ];

    # Created before files are written (activation step 4 precedes step 5), so
    # the env file below has somewhere to land on a host that has never run the
    # checkout unit.
    nixfleet.directories.${cfg.recipeDir} = {
      mode = "0755";
      owner = cfg.user;
      group = cfg.user;
    };

    nixfleet.files."${cfg.recipeDir}/.env.dspark" = {
      mode = "0644";
      owner = cfg.user;
      group = cfg.user;
      text = ''
        # Managed by NixFleet — modules/dspark-dsv4.nix (do not edit).
        ${envText}
      '';
      # Head only: restarting the worker's unit would be a no-op (it has none),
      # and the head's restart takes both containers down and back up anyway.
      restartUnits = lib.optionals isHead [ "dspark-dsv4.service" ];
    };

    nixfleet.systemd.units = {
      # Materializes the pinned recipe. Deliberately not a `git clone`: the
      # directory already exists (declared above) and holds the env file, and
      # clone refuses a non-empty target. init+fetch+checkout is idempotent and
      # leaves untracked files alone.
      "dspark-recipe.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Materialize the dspark recipe at ${cfg.recipeRev}
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=oneshot
          RemainAfterExit=yes
          User=${cfg.user}
          WorkingDirectory=${cfg.recipeDir}
          ExecStart=/bin/bash -c '\
            set -eu; \
            git rev-parse --git-dir >/dev/null 2>&1 || git init -q; \
            git remote get-url origin >/dev/null 2>&1 || git remote add origin ${cfg.recipeUrl}; \
            git remote set-url origin ${cfg.recipeUrl}; \
            if [ "$(git rev-parse --short HEAD 2>/dev/null || true)" != "${cfg.recipeRev}" ]; then \
              git fetch -q --depth 50 origin; \
              git checkout -q --detach ${cfg.recipeRev}; \
            fi; \
            test "$(git rev-parse --short HEAD)" = "${cfg.recipeRev}"'

          [Install]
          WantedBy=multi-user.target
        '';
      };
    }
    // lib.optionalAttrs isHead {
      "dspark-dsv4.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=DeepSeek-V4-Flash — TP=2 across the stacked DGX Spark pair
          After=docker.service network-online.target dspark-recipe.service
          Requires=docker.service
          Wants=network-online.target dspark-recipe.service
          ConditionPathExists=${startScript}

          [Service]
          Type=oneshot
          # Safe here, unlike the .path-triggered appliers in modules/sysctl.nix:
          # nothing starts this unit on a timer or a path change, and the file
          # above restarts it (systemctl restart, not start).
          RemainAfterExit=yes
          User=${cfg.user}
          WorkingDirectory=${cfg.recipeDir}
          # systemd does not set HOME from User=, and the recipe resolves both
          # the HF cache and the SSH identity for the worker out of $HOME.
          Environment=HOME=/home/${cfg.user}
          ExecStart=${startScript}
          ExecStop=${stopScript}
          # Both ranks come up detached (`up -d`), so this returns well before
          # the model is loaded — but a cold start still has to pull layers and
          # mmap 156GB of weights, and the script waits on the worker's SSH.
          TimeoutStartSec=1800
          Restart=no

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      dspark-recipe-pinned = {
        type = "command";
        command = "test \"$(git -C ${cfg.recipeDir} rev-parse --short HEAD)\" = '${cfg.recipeRev}'";
        timeout = 10;
      };

      # Asserts the running container was built from the digest this config
      # names. Compares the image's own sha256, not the reference string, so it
      # stays green whether the image was pulled from our mirror or from
      # upstream — those are byte-identical — but goes red on a genuinely
      # different build left behind by a hand-run `docker compose up`.
      dspark-image-pinned = {
        type = "command";
        command =
          let
            digest = lib.last (lib.splitString "@" cfg.image);
          in
          "test \"$(docker inspect --format '{{index .RepoDigests 0}}' "
          + "$(docker inspect --format '{{.Image}}' "
          + "$(docker ps -q --filter name=vllm-dspark | head -1)) "
          + "| sed 's/.*@//')\" = '${digest}'";
        timeout = 15;
      };
    }
    // lib.optionalAttrs isHead {
      dspark-dsv4-serving = {
        type = "command";
        command = "curl -sf -m 10 http://127.0.0.1:${
          cfg.settings.VLLM_PORT or "8888"
        }/v1/models >/dev/null";
        timeout = 20;
      };
    };
  };
}
