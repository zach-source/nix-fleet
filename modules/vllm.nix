# NixFleet vLLM module
#
# Sibling to modules/llm-inference.nix, which is llama.cpp + ROCm + /dev/kfd
# specific and doesn't stretch to CUDA. This one runs `vllm serve`-style
# entrypoints, including multi-node tensor parallel across DGX Sparks.
#
# The multi-node case is the reason this module exists rather than a couple of
# hand-written units. Every rank runs a byte-identical command except for
# --node-rank, so the flag set lives in one place and each host states only
# which rank it is. Divergence between ranks is the classic way a TP job hangs
# in NCCL init with no useful error.
#
# What this module does NOT do: install vLLM. Builds like
# 0.21.1rc1.dev339+g<sha> are git-pinned dev wheels, and entrypoints such as
# dsv4-vllm-entrypoint ship with a vendor recipe rather than nixpkgs. Point
# `entrypoint` at whatever is already on the box (venv, container shim, or a
# wrapper on PATH) — declaring it here would be a lie about provenance.
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.vllm;

  serviceType = lib.types.submodule (
    { name, ... }:
    {
      options = {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
          description = "Whether to run this vLLM service.";
        };

        description = lib.mkOption {
          type = lib.types.str;
          default = "vLLM ${name}";
          description = "systemd unit description.";
        };

        entrypoint = lib.mkOption {
          type = lib.types.str;
          example = "/opt/dsv4/bin/dsv4-vllm-entrypoint";
          description = ''
            Absolute path to the vLLM entrypoint. Not packaged by NixFleet —
            see the module header. Prefer an absolute path over a bare name so
            the unit doesn't depend on systemd's PATH.
          '';
        };

        model = lib.mkOption {
          type = lib.types.str;
          example = "deepseek-ai/DeepSeek-V4-Flash";
          description = "Model reference passed to `serve` (HF repo id or local path).";
        };

        servedModelName = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Name clients address the model by (--served-model-name).";
        };

        host = lib.mkOption {
          type = lib.types.str;
          default = "0.0.0.0";
          description = "Bind address for the OpenAI-compatible server.";
        };

        port = lib.mkOption {
          type = lib.types.port;
          default = 8000;
          description = "Bind port. Only the rank-0 node actually serves HTTP.";
        };

        user = lib.mkOption {
          type = lib.types.str;
          default = "root";
          description = "User the unit runs as.";
        };

        environment = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          example = {
            HF_TOKEN = "hf_...";
            NCCL_SOCKET_IFNAME = "enp1s0f1np1";
          };
          description = ''
            Environment for the unit. NCCL_SOCKET_IFNAME is worth setting on a
            multi-node job: without it NCCL picks an interface by its own
            heuristics and can select the management NIC instead of the
            200GbE ConnectX-7 fabric, which "works" but runs at 1/100th the
            bandwidth.

            Avoid putting secrets here — unit files are world-readable. Use
            nixfleet.secrets and an EnvironmentFile for tokens.
          '';
        };

        environmentFile = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Optional EnvironmentFile, e.g. a decrypted secret holding HF_TOKEN.";
        };

        cluster = {
          enable = lib.mkEnableOption "multi-node tensor/pipeline parallelism";

          nnodes = lib.mkOption {
            type = lib.types.int;
            default = 2;
            description = "Total nodes in the job (--nnodes). Same on every rank.";
          };

          nodeRank = lib.mkOption {
            type = lib.types.int;
            example = 0;
            description = ''
              This node's rank (--node-rank). Rank 0 is the master and the only
              one that serves HTTP; the others join it. Must be unique and
              contiguous from 0 across the job.
            '';
          };

          masterAddr = lib.mkOption {
            type = lib.types.nullOr lib.types.str;
            default = null;
            example = "192.168.100.10";
            description = ''
              Address of rank 0, as reachable over the cluster fabric — on a
              DGX Spark pair that is the ConnectX-7 address from
              modules/dgx-spark-cluster.nix, not the management IP.

              Passed as --master-addr. Optional on rank 0, which can infer
              itself, but required on every other rank.
            '';
          };

          masterPort = lib.mkOption {
            type = lib.types.port;
            default = 25000;
            description = "Rendezvous port (--master-port). Identical on every rank.";
          };
        };

        extraFlags = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          description = ''
            Flags appended verbatim after the module-generated ones. Values
            containing shell metacharacters (notably --speculative-config, whose
            argument is JSON) must be quoted by the caller; this module does not
            re-quote them.
          '';
        };
      };
    }
  );

  mkExecStart =
    svc:
    let
      clusterFlags = lib.optionals svc.cluster.enable (
        [
          "--nnodes"
          (toString svc.cluster.nnodes)
          "--node-rank"
          (toString svc.cluster.nodeRank)
          "--master-port"
          (toString svc.cluster.masterPort)
        ]
        ++ lib.optionals (svc.cluster.masterAddr != null) [
          "--master-addr"
          svc.cluster.masterAddr
        ]
      );
      flags = [
        "serve"
        svc.model
      ]
      ++ lib.optionals (svc.servedModelName != null) [
        "--served-model-name"
        svc.servedModelName
      ]
      ++ [
        "--host"
        svc.host
        "--port"
        (toString svc.port)
      ]
      ++ svc.extraFlags
      ++ clusterFlags;
    in
    "${svc.entrypoint} ${lib.concatStringsSep " " flags}";

  mkUnit = name: svc: {
    name = "vllm-${name}.service";
    value = {
      enabled = true;
      text = ''
        [Unit]
        Description=${svc.description}
        After=network-online.target
        Wants=network-online.target

        [Service]
        Type=simple
        User=${svc.user}
        ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "Environment=${k}=${v}") svc.environment)}
        ${lib.optionalString (svc.environmentFile != null) "EnvironmentFile=${svc.environmentFile}"}
        ExecStart=${mkExecStart svc}
        Restart=on-failure
        RestartSec=10
        # A multi-node rank blocks in NCCL rendezvous until its peers appear,
        # and a 1M-context model is slow to load besides. systemd's 90s default
        # would kill it mid-startup and look like a crash loop.
        TimeoutStartSec=1800
        # vLLM wants far more than the default 1024 descriptors once prefix
        # caching and a large block table are live.
        LimitNOFILE=65535
        # Shared memory is how --distributed-executor-backend mp moves tensors
        # between local workers; the container-ish default of 64MB is not enough.
        PrivateTmp=false

        [Install]
        WantedBy=multi-user.target
      '';
    };
  };

  enabledServices = lib.filterAttrs (_: svc: svc.enable) cfg.services;
in

{
  options.nixfleet.modules.vllm = {
    enable = lib.mkEnableOption "vLLM inference services";

    services = lib.mkOption {
      type = lib.types.attrsOf serviceType;
      default = { };
      description = "vLLM services to run on this host.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = lib.flatten (
      lib.mapAttrsToList (name: svc: [
        {
          assertion = !svc.cluster.enable || svc.cluster.nodeRank < svc.cluster.nnodes;
          message = "vllm.${name}: nodeRank ${toString svc.cluster.nodeRank} must be < nnodes ${toString svc.cluster.nnodes}";
        }
        {
          # Ranks other than 0 have nothing to rendezvous with otherwise, and
          # the failure is a silent hang rather than an error.
          assertion = !svc.cluster.enable || svc.cluster.nodeRank == 0 || svc.cluster.masterAddr != null;
          message = "vllm.${name}: cluster.masterAddr is required on rank ${toString svc.cluster.nodeRank} (only rank 0 may omit it)";
        }
      ]) cfg.services
    );

    nixfleet.systemd.units = lib.listToAttrs (lib.mapAttrsToList mkUnit enabledServices);

    nixfleet.healthChecks = lib.mapAttrs' (
      name: svc:
      lib.nameValuePair "vllm-${name}" {
        # Only rank 0 serves HTTP; other ranks are workers with no endpoint, so
        # health-check the unit rather than a port there.
        type = if (!svc.cluster.enable || svc.cluster.nodeRank == 0) then "http" else "command";
        url =
          if (!svc.cluster.enable || svc.cluster.nodeRank == 0) then
            "http://localhost:${toString svc.port}/health"
          else
            null;
        command =
          if (svc.cluster.enable && svc.cluster.nodeRank != 0) then
            "systemctl is-active --quiet vllm-${name}.service"
          else
            null;
        # Model load at 1M context is slow; don't flap during it.
        timeout = 120;
      }
    ) enabledServices;
  };
}
