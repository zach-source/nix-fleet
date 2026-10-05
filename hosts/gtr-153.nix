# GTR-153 — AMD Ryzen AI MAX+ 395 (192.168.3.130)
# The THINKING tier (2026-10-05): one model, Qwen3.8-27B dense with MTP
# self-speculation and thinking on, plus the Judge0 code execution sandbox.
# One model per box so the rest of the unified memory stays free for builds
# and hosting.
# Note: Judge0 runs via Docker Compose (needs privileged containers for isolate)
# History: previously hosted MiniMax-M2.7 229B; removed to try DeepSeek
# V4-Flash, which proved un-runnable on AMD (CUDA/Metal-only dsv4 ops).
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/llm-inference.nix
    ../modules/ufw.nix
    ../modules/iscsi.nix
    ../modules/multipath.nix
    ../modules/backup.nix
    ../modules/k0s.nix
    ../modules/kubevirt.nix
    ../modules/sysctl.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-153";
      base = "ubuntu";
      addr = "192.168.3.130";
    };

    # k0s worker, declaratively managed. system-reserved=56Gi -> ~66Gi k8s
    # allocatable for builds and hosting. Was 78Gi (~44Gi allocatable) while
    # this box ran several models; one model per box since 2026-10-05 needs
    # ~30-45Gi, and 56Gi keeps ~10-15Gi of host headroom on top of it.
    k0s.worker = {
      enable = true;
      systemReservedMemory = "56Gi";
    };

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    # Blacklist Synology LUNs from dm-multipath auto-claim — see
    # modules/multipath.nix (2026-07-25 gastown-town readonly incident).
    modules.multipath.enable = true;

    # KubeVirt: bind k0s's real kubelet pods dir onto the hardcoded
    # /var/lib/kubelet/pods, or virt-launcher's container-disk init container
    # can't find its binary and every VM crash-loops. See modules/kubevirt.nix.
    modules.kubevirt.enable = true;

    # inotify headroom for k0s. Ubuntu defaults (max_user_instances=128) are
    # exhausted by kubelet + containerd + CSI, and virt-handler then panics at
    # startup with "Failed to create an inotify watcher: too many open files".
    # Same values gti already runs.
    modules.sysctl = {
      enable = true;
      settings = {
        "fs.inotify.max_user_watches" = 524288;
        "fs.inotify.max_user_instances" = 8192;
      };
    };

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
      # Qwen3.8-27B DENSE — succeeds the Qwen3.6-27B that held this slot
      # (:8085) from 2026-07-17 until it was disabled 2026-07-27 for memory.
      # Same GGUF architecture string (`qwen35`) as the 3.6 it replaces, so the
      # existing /opt/llama-rocm-latest build loads it unchanged; the old 3.6
      # GGUF stays on disk for a one-line revert.
      #
      # Deliberately Q5_K_XL (20.2GB), not the Q6_K_XL (25.9GB) the 3.6 used:
      # this node co-hosts hauhaucs-uncensored + k0s pods and was swap-thrashing
      # at the 26GB size. ctxSize also stays at the slot's proven 131072 even
      # though 3.8 is natively 262144 — raise it only after watching `free -g`
      # here, since the hybrid Gated-DeltaNet arch keeps KV cheap (only 16 of 64
      # layers are full attention) and the headroom may well be there.
      services.qwen38-27b = {
        description = "Qwen3.8-27B dense (quality/coding/agentic) + MTP self-speculation";
        model = "/srv/models/Qwen3.8-27B-UD-Q5_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8085;
        # 512K context on a natively-262K model: static YaRN, factor 2 — the
        # factor Qwen's cards give for 524288. Same window on every gtr box
        # (2026-10-05). Static YaRN applies at every length, so very short
        # prompts may lose a little quality; drop the three rope/yarn flags
        # and set ctxSize = 262144 to undo.
        ctxSize = 524288;
        # CORRECTION 2026-08-15: this GGUF already carries its own MTP head —
        # `strings` on the file shows qwen35.nextn_predict_layers and
        # blk.64.nextn.* tensors. An earlier revision also passed
        # `--spec-draft-model /srv/models/mtp-Qwen3.8-27B-Q8_0.gguf`, which
        # overrode that built-in head with ggml-org's Q8_0 one against these
        # Q5_K_XL weights. Measured draft acceptance was 57% on a code-gen
        # benchmark, against 81% for gtr-151's single-repo merged-MTP build on
        # the same workload. The external draft file is now dropped; it stays
        # on disk but is unused.
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        extraFlags = [
          # --fit off: gtr-153's /srv is ZFS, and the auto memory-fit step
          # re-reads the whole GGUF to measure (~8min cold load observed).
          "--fit"
          "off"
          # Qwen3.8 model card, thinking mode (3.6 used temp 0.6; 3.8 asks 1.0).
          "--temp"
          "1.0"
          "--top-p"
          "0.95"
          "--top-k"
          "20"
          "--min-p"
          "0.0"
          "--rope-scaling"
          "yarn"
          "--rope-scale"
          "2"
          "--yarn-orig-ctx"
          "262144"
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
          port = 8085;
          comment = "Qwen3.8-27B llama-server from LAN/cluster (LiteLLM gateway)";
        }
        # The LiteLLM pod egresses to this node WITHOUT SNAT (arrives with the
        # k8s pod-CIDR source, not the node IP), so the LAN rules above don't
        # match it and default-deny drops it. Allow the pod CIDR explicitly for
        # the inference port (same pattern as :18080 below).
        {
          from = "10.244.0.0/16";
          port = 8085;
          comment = "Qwen3.8-27B llama-server from k8s pod CIDR (LiteLLM)";
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
