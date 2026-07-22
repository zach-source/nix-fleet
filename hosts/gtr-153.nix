# GTR-153 — AMD Ryzen AI MAX+ 395 (192.168.3.130)
# Qwopus3.5-9B-Coder + Judge0 code execution sandbox
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
# Note: Judge0 runs via Docker Compose (needs privileged containers for isolate)
# History: previously hosted MiniMax-M2.7 229B; removed to try DeepSeek
# V4-Flash, which proved un-runnable on AMD (CUDA/Metal-only dsv4 ops).
{ pkgs, ... }:

{
  imports = [
    ../modules/llm-inference.nix
    ../modules/ufw.nix
    ../modules/iscsi.nix
    ../modules/backup.nix
    ../modules/k0s.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-153";
      base = "ubuntu";
      addr = "192.168.3.130";
    };

    # k0s worker, declaratively managed. system-reserved=78Gi -> ~44Gi k8s
    # allocatable (was an out-of-band 98Gi/24Gi); 78Gi stays for inference.
    k0s.worker.enable = true;

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    packages = with pkgs; [
      git
      htop
      curl
      jq
      tmux
      vim
    ];

    # Preserve gtr-153's pre-existing nix system-features (the nix-config
    # module owns nix.custom.conf, so they'd otherwise be dropped). trusted-users
    # uses the module default (root @wheel ztaylor deploy).
    modules.nixConfig.systemFeatures = [
      "nixos-test"
      "benchmark"
      "big-parallel"
      "kvm"
    ];

    modules.llmInference = {
      enable = true;
      services.qwopus35-coder = {
        description = "Qwopus3.5-9B-Coder (qwen35, Q8_0)";
        # qwen35 arch (Qwen3.5 hybrid). Runs on the stock /opt/llama-rocm
        # build (its arch table includes qwen35; 9B needs no speculation).
        model = "/srv/models/qwopus35-9b-coder/Qwopus3.5-9B-coder-Exp-Q8_0.gguf";
        port = 8082;
        ctxSize = 131072; # 9B is small — plenty of room on the 122GB node
        cacheReuse = 256;
        jinja = true;
        reasoning = {
          format = "deepseek";
          # 512 (was 2048): 2048 thinking tokens at ~25 tok/s made every turn
          # ~80s+, blowing agent-chain timeouts. 512 keeps qwopus usable
          # interactively.
          budget = 512;
        };
        # Sampler nudge — see hosts/gtr-152.nix / docs/llm-proxy-usage.md.
        # mmproj for vision available at
        # /srv/models/qwopus35-9b-coder/mmproj.gguf — add via extraFlags
        # (--mmproj) once stock-build multimodal support is confirmed.
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };

      # Qwen3.6-27B DENSE — MOVED here from gtr-151 (2026-07-17) to use this
      # node's otherwise-idle GPU and relieve gtr-151's 3-model contention.
      # Needs the latest-upstream gfx1151 build (/opt/llama-rocm-latest) + its
      # pinned /opt/rocm-sdk, both staged onto gtr-153 alongside the 25GB MTP
      # GGUF. MTP self-speculation (pure 3.6, no draft model).
      services.qwen36-27b = {
        description = "Qwen3.6-27B dense (quality/coding) + MTP self-speculation";
        model = "/srv/models/Qwen3.6-27B-MTP-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8085;
        ctxSize = 131072;
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        extraFlags = [
          "--temp"
          "0.6"
          "--top-p"
          "0.95"
          "--top-k"
          "20"
          "--min-p"
          "0.0"
        ];
      };
    };

    # UFW rules — gtr-153 has ufw active (vestigial k0s-node setup, unlike
    # the other gtr boxes where ufw is inactive). The default-deny incoming
    # was silently blocking the LiteLLM gateway (SNATs from gti 192.168.3.131)
    # from reaching the llama-server, so it never appeared routable in the
    # fleet gateway. Declared here so it survives re-deploys.
    modules.ufw = {
      enable = true;
      rules = [
        {
          from = "192.168.0.0/16";
          port = 8082;
          comment = "Qwopus llama-server from LAN/cluster (LiteLLM gateway)";
        }
        {
          from = "192.168.0.0/16";
          port = 8085;
          comment = "Qwen3.6-27B llama-server from LAN/cluster (moved from gtr-151)";
        }
        {
          from = "10.244.0.0/16";
          port = 18080;
          comment = "archive-v6-proxy: pods → node-local apt IPv6-egress proxy";
        }
      ];
    };

    # Judge0 code execution sandbox — deployed via Docker Compose
    # Config at /opt/judge0/{docker-compose.yml,judge0.conf}
    # API: http://192.168.3.130:2358
    # Auth: X-Auth-Token: nixfleet-judge0-auth
    # 47 languages, sandboxed via Linux isolate
    files."/opt/judge0/judge0.conf" = {
      text = ''
        POSTGRES_HOST=db
        POSTGRES_PORT=5432
        POSTGRES_DB=judge0
        POSTGRES_USER=judge0
        POSTGRES_PASSWORD=nixfleet-judge0-2026
        REDIS_HOST=redis
        REDIS_PORT=6379
        REDIS_PASSWORD=nixfleet-redis-2026
        AUTHN_TOKEN=nixfleet-judge0-auth
        AUTHZ_TOKEN=nixfleet-judge0-authz
        ENABLE_PER_PROCESS_AND_THREAD_TIME_LIMIT=true
        ENABLE_PER_PROCESS_AND_THREAD_MEMORY_LIMIT=true
        CPU_TIME_LIMIT=10
        MAX_CPU_TIME_LIMIT=30
        WALL_TIME_LIMIT=30
        MAX_WALL_TIME_LIMIT=60
        MEMORY_LIMIT=256000
        MAX_MEMORY_LIMIT=512000
        MAX_PROCESSES_AND_OR_THREADS=120
        ENABLE_BATCHED_SUBMISSIONS=true
      '';
      owner = "deploy";
      group = "deploy";
    };

    files."/opt/judge0/docker-compose.yml" = {
      text = ''
        services:
          server:
            image: judge0/judge0:latest
            ports:
              - "2358:2358"
            privileged: true
            env_file: judge0.conf
            environment:
              - POSTGRES_HOST=db
              - REDIS_HOST=redis
            restart: unless-stopped
            depends_on:
              db:
                condition: service_started
              redis:
                condition: service_started
          worker:
            image: judge0/judge0:latest
            command: ./scripts/workers
            privileged: true
            env_file: judge0.conf
            environment:
              - POSTGRES_HOST=db
              - REDIS_HOST=redis
            restart: unless-stopped
            depends_on:
              db:
                condition: service_started
              redis:
                condition: service_started
          db:
            image: postgres:16
            environment:
              - POSTGRES_DB=judge0
              - POSTGRES_USER=judge0
              - POSTGRES_PASSWORD=nixfleet-judge0-2026
            volumes:
              - judge0-db:/var/lib/postgresql/data
            restart: unless-stopped
          redis:
            image: redis:7
            command: ["redis-server", "--requirepass", "nixfleet-redis-2026", "--appendonly", "no"]
            restart: unless-stopped
        volumes:
          judge0-db:
      '';
      owner = "deploy";
      group = "deploy";
    };
  };
}
