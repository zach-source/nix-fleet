# LLM Inference module for NixFleet
# Generates llama-server systemd services from declarative config.
#
# Usage in host config:
#   nixfleet.modules.llmInference = {
#     enable = true;
#     rocmLibPath = "/opt/llama-rocm";
#     services.qwen35 = {
#       model = "/srv/models/Qwen3.5-35B-A3B-Q4_K_M.gguf";
#       port = 8084;
#       ctxSize = 200000;
#       cacheTypeK = "q4_0";
#       cacheTypeV = "q4_0";
#       draft = { model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf"; max = 4; pMin = 0.6; };
#       reasoning = { format = "deepseek"; budget = 2048; };
#     };
#   };
{ config, lib, ... }:

with lib;

let
  cfg = config.nixfleet.modules.llmInference;

  # Draft model options
  draftType = types.submodule {
    options = {
      model = mkOption {
        type = types.str;
        description = "Path to draft model GGUF";
      };
      max = mkOption {
        type = types.int;
        default = 4;
        description = "Max draft tokens";
      };
      min = mkOption {
        type = types.int;
        default = 1;
        description = "Min draft tokens";
      };
      pMin = mkOption {
        type = types.float;
        default = 0.6;
        description = "Min draft probability";
      };
    };
  };

  # Reasoning options
  reasoningType = types.submodule {
    options = {
      format = mkOption {
        type = types.enum [
          "deepseek"
          "none"
        ];
        default = "deepseek";
      };
      budget = mkOption {
        type = types.int;
        default = 2048;
        description = "Thinking token budget";
      };
    };
  };

  # Per-service options
  serviceType = types.submodule (
    { name, ... }:
    {
      options = {
        enable = mkOption {
          type = types.bool;
          default = true;
        };
        description = mkOption {
          type = types.str;
          default = "llama.cpp ROCm (${name})";
        };
        model = mkOption {
          type = types.str;
          description = "Path to model GGUF";
        };
        port = mkOption {
          type = types.int;
          description = "Listen port";
        };
        binary = mkOption {
          type = types.str;
          default = "${cfg.rocmLibPath}/llama-server";
        };
        ldLibraryPath = mkOption {
          type = types.str;
          default = cfg.rocmLibPath;
        };

        # Context & batching
        ctxSize = mkOption {
          type = types.int;
          default = 131072;
        };
        batchSize = mkOption {
          type = types.int;
          default = 2048;
        };
        ubatchSize = mkOption {
          type = types.int;
          default = 2048;
        };
        cacheTypeK = mkOption {
          type = types.str;
          default = "q4_0";
        };
        cacheTypeV = mkOption {
          type = types.str;
          default = "q4_0";
        };
        ctxCheckpoints = mkOption {
          type = types.int;
          default = 32;
        };
        cacheReuse = mkOption {
          type = types.nullOr types.int;
          default = 256;
        };

        # GPU
        gpuLayers = mkOption {
          type = types.int;
          default = 99;
        };
        flashAttn = mkOption {
          type = types.bool;
          default = true;
        };
        noMmap = mkOption {
          type = types.bool;
          default = false;
        };
        mlock = mkOption {
          type = types.bool;
          default = false;
        };

        # Threading
        threads = mkOption {
          type = types.int;
          default = 8;
        };
        threadsBatch = mkOption {
          type = types.int;
          default = 16;
        };
        parallel = mkOption {
          type = types.int;
          default = 1;
        };

        # Speculation
        draft = mkOption {
          type = types.nullOr draftType;
          default = null;
        };

        # Reasoning
        reasoning = mkOption {
          type = types.nullOr reasoningType;
          default = null;
        };

        # Embedding mode
        embedding = mkOption {
          type = types.bool;
          default = false;
        };

        # Chat
        jinja = mkOption {
          type = types.bool;
          default = true;
        };

        # Extra flags
        extraFlags = mkOption {
          type = types.listOf types.str;
          default = [ ];
        };

        # ROCm env
        rocmEnv = mkOption {
          type = types.attrsOf types.str;
          default = {
            GGML_CUDA_FORCE_MMQ = "1";
            HIP_FORCE_DEV_KERNARG = "1";
            GPU_MAX_HW_QUEUES = "8";
            HSA_ENABLE_SDMA = "0";
          };
        };
      };
    }
  );

  # Generate ExecStart args for a service
  mkExecStart =
    name: svc:
    let
      flags = [
        "--model ${svc.model}"
        "--host 0.0.0.0 --port ${toString svc.port}"
        "--ctx-size ${toString svc.ctxSize}"
        "--batch-size ${toString svc.batchSize}"
        "--ubatch-size ${toString svc.ubatchSize}"
        "--cache-type-k ${svc.cacheTypeK}"
        "--cache-type-v ${svc.cacheTypeV}"
        "--threads ${toString svc.threads}"
        "--threads-batch ${toString svc.threadsBatch}"
        "-ngl ${toString svc.gpuLayers}"
        "--parallel ${toString svc.parallel}"
      ]
      ++ optional svc.flashAttn "--flash-attn on"
      ++ optional svc.noMmap "--no-mmap"
      ++ optional svc.mlock "--mlock"
      ++ optional svc.jinja "--jinja"
      ++ optional svc.embedding "--embedding"
      ++ optional (svc.cacheReuse != null) "--cache-reuse ${toString svc.cacheReuse}"
      ++ optional (svc.ctxCheckpoints != 32) "--ctx-checkpoints ${toString svc.ctxCheckpoints}"
      ++ optionals (svc.draft != null) [
        "--model-draft ${svc.draft.model}"
        "--gpu-layers-draft 99"
        "--draft-max ${toString svc.draft.max}"
        "--draft-min ${toString svc.draft.min}"
        "--draft-p-min ${toString svc.draft.pMin}"
      ]
      ++ optionals (svc.reasoning != null && svc.reasoning.format != "none") [
        "--reasoning-format ${svc.reasoning.format}"
        "--reasoning-budget ${toString svc.reasoning.budget}"
      ]
      ++ svc.extraFlags;
    in
    "${svc.binary} \\\n  ${concatStringsSep " \\\n  " flags}";

  # Generate systemd unit text
  mkUnit = name: svc: ''
    [Unit]
    Description=${svc.description}
    After=network.target
    Wants=dev-kfd.device
    After=dev-kfd.device

    [Service]
    Type=simple
    User=deploy
    SupplementaryGroups=render video
    Environment=LD_LIBRARY_PATH=${svc.ldLibraryPath}
    ${concatStringsSep "\n" (mapAttrsToList (k: v: "Environment=${k}=${v}") svc.rocmEnv)}
    ExecStartPre=/bin/bash -c 'for i in $(seq 1 30); do [ -e /dev/kfd ] && exit 0; sleep 1; done; echo "WARNING: /dev/kfd not found after 30s"'
    ExecStart=${mkExecStart name svc}
    Restart=on-failure
    RestartSec=10
    LimitMEMLOCK=infinity

    [Install]
    WantedBy=multi-user.target
  '';

in
{
  options.nixfleet.modules.llmInference = {
    enable = mkEnableOption "LLM inference services";

    rocmLibPath = mkOption {
      type = types.str;
      default = "/opt/llama-rocm";
      description = "Path to ROCm-enabled llama.cpp libs";
    };

    services = mkOption {
      type = types.attrsOf serviceType;
      default = { };
      description = "LLM inference services to deploy";
    };
  };

  config = mkIf cfg.enable {
    nixfleet.systemd.units = mapAttrs' (
      name: svc:
      nameValuePair "llama-rocm-${name}.service" {
        text = mkUnit name svc;
        enabled = svc.enable;
      }
    ) cfg.services;
  };
}
