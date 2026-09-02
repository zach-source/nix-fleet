# NixFleet GLM-5.3-Flash module (MiaAI-Lab EXL3 recipe, Docker + Ray)
#
# Runs GLM-5.3-Flash (320B total / 18B active MoE) TP=2 across the stacked DGX
# Spark pair, from the EXL3 4bpw checkpoint. Same shape as modules/dspark-dsv4.nix
# and by the same upstream lab, so the two read alike on purpose: pinned recipe
# checkout, a rendered env file, head-only service unit, health checks.
#
# Why EXL3 and not NVFP4, which is the obvious choice for a Blackwell box:
# GB10 reports compute capability 12.1 (verified on spark-5267). vLLM's fast
# NVFP4 fused-MoE kernels are CUTLASS tcgen05 block-scaled MMA, which targets
# sm_100a — datacenter Blackwell. sm_121 has no such instruction, so vLLM falls
# back to marlin dequant: you pay 4-bit's accuracy cost and get none of its
# speed. Measured on 2x Spark, NVFP4 lands at 23-30 tok/s against EXL3's ~63,
# and an independent teacher-logit panel puts EXL3 4bpw at 0.0246 mean KLD
# (indistinguishable from the official FP8 checkpoint's 0.0246) versus NVFP4's
# 0.0605 at the same file size. EXL3 wins on both axes, which is unusual and
# worth stating so nobody "upgrades" this to NVFP4 later.
#
# This model and DeepSeek-V4-Flash cannot coexist in memory: each wants ~80 GiB
# of the 121 GiB per node. They are mutually exclusive units — see Conflicts=
# below — so switching is a systemctl start, not a re-download.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.dsparkGlm53;

  isHead = cfg.nodeRank == 0;

  allSettings = cfg.settings // {
    IMAGE = cfg.image;
  };

  envText = lib.concatStringsSep "\n" (
    lib.mapAttrsToList (k: v: "${k}=${v}") (lib.filterAttrs (_: v: v != null) allSettings)
  );

  hfCache = "/home/${cfg.user}/.cache/huggingface";

  # Derived from the settings rather than written out, so the weights gate below
  # cannot drift from the checkpoint actually being served. The Hub's on-disk
  # layout turns "org/name" into "models--org--name".
  modelCacheName = "models--" + builtins.replaceStrings [ "/" ] [ "--" ] cfg.settings.MODEL;
  weightsPath = "${hfCache}/hub/${modelCacheName}/snapshots/${cfg.settings.MODEL_REVISION}";

  startScript = "${cfg.recipeDir}/start.sh";
  stopScript = "${cfg.recipeDir}/stop.sh";
in
{
  options.nixfleet.modules.dsparkGlm53 = {
    enable = lib.mkEnableOption "GLM-5.3-Flash via the MiaAI-Lab EXL3 recipe";

    nodeRank = lib.mkOption {
      type = lib.types.int;
      description = ''
        0 = head (serves HTTP, starts the worker over SSH), 1 = worker.
        The worker needs the checkout and the env file but gets no service unit;
        the head launches rank 1 itself over the fabric.
      '';
      example = 0;
    };

    image = lib.mkOption {
      type = lib.types.str;
      default = "ghcr.io/miaai-lab/glm-5.3-flash-2x-dgx-sparks:exl3@sha256:9bb1557a4234fce63d59599e44d10747eabd742beb337eebf9e7070be8a0fd58";
      description = ''
        Serving image, pinned by digest so an upstream retag cannot silently
        change the runtime under us.

        Unlike the dsv4 image this one is anonymously pullable (verified against
        the GHCR token endpoint), so there is no read:packages credential to
        provision and no private mirror to keep in sync. If that ever changes,
        the recipe's own start.sh can rebuild the image from its Dockerfile.
      '';
    };

    recipeUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://github.com/MiaAI-Lab/GLM-5.3-Flash-EXL3-2x-DGX-Sparks";
      description = "Upstream recipe repository.";
    };

    recipeRev = lib.mkOption {
      type = lib.types.str;
      default = "eb0469fbb2b49fd7c025f594a3339a121e58f7a9";
      description = ''
        Recipe commit to check out. Pinned to a full SHA rather than a branch:
        start.sh, stop.sh and the overlay/ patches all ship from here and are
        mounted into the container, so tracking main would change the runtime on
        an unrelated deploy. This tree moves fast — it gained a 20% prefill
        kernel and a TP4 path within days — which is exactly why it is pinned.
      '';
    };

    recipeDir = lib.mkOption {
      type = lib.types.str;
      default = "/opt/glm53";
      description = ''
        Checkout location, and where start.sh sources its env file from. Must be
        identical on both nodes — the head runs the worker's launch over SSH in
        the worker's own copy of this directory.

        Deliberately not /opt/dspark: that belongs to the dsv4 recipe, and the
        two must be able to sit on disk at the same time.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "deploy";
      description = ''
        User that owns the checkout and runs the stack. Needs docker group
        membership, which this module declares, and passwordless SSH to the
        worker over the fabric — already true on this pair for dsv4.
      '';
    };

    settings = lib.mkOption {
      type = lib.types.attrsOf (lib.types.nullOr lib.types.str);
      default = { };
      description = ''
        Contents of the recipe's env file, rendered one KEY=value per line.
        Set a key to null to omit it. See .env.example in the recipe for the
        full surface.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.packages = with pkgs; [ git ];

    nixfleet.users.${cfg.user}.extraGroups = [ "docker" ];

    nixfleet.directories.${cfg.recipeDir} = {
      mode = "0755";
      owner = cfg.user;
      group = cfg.user;
    };

    nixfleet.files."${cfg.recipeDir}/.env" = {
      mode = "0644";
      owner = cfg.user;
      group = cfg.user;
      text = ''
        # Managed by NixFleet — modules/dspark-glm53.nix (do not edit).
        ${envText}
      '';
      restartUnits = lib.optionals isHead [ "glm53-flash.service" ];
    };

    nixfleet.systemd.units = {
      "glm53-recipe.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Materialize the GLM-5.3-Flash EXL3 recipe at ${cfg.recipeRev}
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
            if [ "$(git rev-parse HEAD 2>/dev/null || true)" != "${cfg.recipeRev}" ]; then \
              git fetch -q --depth 50 origin; \
              git checkout -q --detach ${cfg.recipeRev}; \
            fi; \
            test "$(git rev-parse HEAD)" = "${cfg.recipeRev}"'

          [Install]
          WantedBy=multi-user.target
        '';
      };
    }
    // lib.optionalAttrs isHead {
      "glm53-flash.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=GLM-5.3-Flash EXL3 — TP=2 across the stacked DGX Spark pair
          After=docker.service network-online.target glm53-recipe.service
          Requires=docker.service
          Wants=network-online.target glm53-recipe.service
          ConditionPathExists=${startScript}
          # The weights gate. 164 GiB has to be on disk before this can serve,
          # and the download is deliberately NOT part of activation — it is an
          # hours-long network fetch that has no business blocking a deploy.
          # Until the pinned snapshot exists systemd skips this unit and says so,
          # which is a clean "not yet" rather than a failed activation.
          ConditionPathExists=${weightsPath}

          [Service]
          Type=oneshot
          RemainAfterExit=yes
          User=${cfg.user}
          WorkingDirectory=${cfg.recipeDir}
          # Mutual exclusion with DeepSeek-V4-Flash: ~80 GiB of the 121 GiB per
          # node each, so they cannot both be resident.
          #
          # This is deliberately ExecStartPre and NOT Conflicts=, which is the
          # obvious way to write it and is wrong here. systemd resolves
          # Conflicts= when it *queues* the job but evaluates Condition*= when it
          # *executes* it, so a Conflicts= on this unit evicts dsv4 even on the
          # activations where the weights gate above skips this unit entirely.
          # That is not theoretical — it took dsv4 down on the first deploy of
          # this module, one second before logging "skipped because unmet
          # condition check". Exec*= lines never run for a skipped unit, so this
          # form only evicts dsv4 when GLM is genuinely starting.
          #
          # The `+` prefix runs this one command as root. Without it the command
          # inherits User= above and a non-root systemctl stop of a system unit
          # dies on "Interactive authentication required" — which fails the whole
          # unit, so GLM would refuse to start rather than start alongside dsv4.
          ExecStartPre=+/usr/bin/systemctl stop dspark-dsv4.service
          # Reclaim page cache before the engine sizes its KV pool, on both
          # nodes. This is not superstition: GB10 is unified memory, so page
          # cache and GPU allocations come out of the same 121 GiB. A cold start
          # follows a 164 GiB weight rsync, which leaves the cache full, and
          # vLLM then measures the shortfall at KV-sizing time and refuses to
          # start ("14.52 GiB KV cache is needed ... available 13.27 GiB").
          # Upstream GB10 recipes document the same drop as a pre-launch ritual.
          # Ordered after the stop above so dsv4's memory is released first.
          ExecStartPre=+/bin/sh -c 'sync; echo 3 > /proc/sys/vm/drop_caches'
          # systemd does not derive HOME from User=, and the recipe resolves the
          # HF cache and the worker's SSH identity out of $HOME.
          Environment=HOME=/home/${cfg.user}
          # Points the recipe's hf-CLI probe at the venv we provision rather than
          # letting it fall through to a PATH lookup that does not exist on a
          # stock DGX OS image. /opt/hf-cli/venv/bin/hf is one of the paths
          # resolve_hf_bin() already searches, so this is belt and braces.
          Environment=HF_BIN=/opt/hf-cli/venv/bin/hf
          ExecStart=${startScript}
          ExecStop=${stopScript}
          # Cold start has to rsync 164 GiB to the worker over the CX7 link and
          # then load a 320B MoE on both ranks. The recipe's own health poll
          # allows 3600s for the load alone.
          TimeoutStartSec=5400
          Restart=no

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      glm53-recipe-pinned = {
        type = "command";
        command = "test \"$(git -C ${cfg.recipeDir} rev-parse HEAD)\" = '${cfg.recipeRev}'";
        timeout = 10;
      };
    }
    // lib.optionalAttrs isHead {
      # Deliberately /health and not /v1/models: this recipe's README is explicit
      # that /v1/models answers 200 with a dead engine behind it, which is the
      # worst possible shape for a health check.
      glm53-flash-serving = {
        type = "command";
        command = "curl -sf -m 10 http://127.0.0.1:${cfg.settings.PORT or "8888"}/health >/dev/null";
        timeout = 20;
      };
    };
  };
}
