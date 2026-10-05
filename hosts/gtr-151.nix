# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# The FAST tier (2026-10-05): one model, Qwen3.6-35B-A3B MoE with a classic
# Qwen3.5-0.8B draft and thinking OFF by default — the fleet's "Haiku".
# One model per box so the rest of the unified memory stays free for builds
# and hosting. Qwen3.8-27B moved off this box (it remains on gtr-153).
# Build: /opt/llama-rocm-latest, ROCm 7.13 (TheRock), gfx1151.
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
    ../modules/multipath.nix
    ../modules/k0s.nix
    ../modules/kubevirt.nix
    ../modules/sysctl.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-151";
      base = "ubuntu";
      addr = "192.168.3.132";
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

    modules.llmInference = {
      enable = true;
      # Runs on LATEST UPSTREAM llama.cpp built for gfx1151 at
      # /opt/llama-rocm-latest (commit 6a257d4). Replaces the old custom fork:
      # upstream PR #19493 natively handles qwen35 hybrid speculation, so the
      # 6 fork patches are obsolete (verified empirically — native spec gives
      # 100% draft acceptance). The build also adds --spec-type draft-mtp.
      #
      # Speculation, after benchmarking AND stability-testing on gfx1151:
      #   MoE (:8084)   — CLASSIC Qwen3.5-0.8B draft (~48 tok/s). MTP on the
      #                   recurrent MoE is UNSTABLE here: it crashes with
      #                   "ROCm error: unspecified launch failure" during
      #                   warmup (same GPU-fault family as the n_max=4 wedge),
      #                   so despite MTP being marginally faster (~61) it's not
      #                   worth the crash/wedge risk. Classic draft is rock-solid.
      #   dense (:8085) — MTP self-speculation (~16 tok/s, pure 3.6) — stable on
      #                   the dense (non-recurrent) arch.
      services.qwen36-spec = {
        description = "Qwen3.6-35B-A3B MoE + classic draft, thinking off (fast tier)";
        # Non-MTP GGUF (UD-Q6_K_XL, 29.7GB).
        model = "/srv/models/Qwen3.6-35B-A3B-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        # 512K context on a natively-262K model: static YaRN, factor 2 — the
        # factor Qwen's cards give for 524288. Same window on every gtr box
        # (2026-10-05). Static YaRN applies at every length, so very short
        # prompts may lose a little quality; drop the rope/yarn/override-kv
        # flags and set ctxSize = 262144 to undo.
        ctxSize = 524288;
        batchSize = 512;
        ubatchSize = 512;
        newCli = true; # new build: --draft-max renamed --spec-draft-n-max
        # No fork workarounds: upstream handles qwen35 recurrent memory + the
        # prompt-cache restore bug. Defaults apply (ctx-checkpoints=32,
        # cache-reuse=256).
        draft = {
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          # n-max 4->6 + pMin 0.6->0.5: benchmarked +8.5% (62.8 -> 68.1 tok/s) on
          # gfx1151 via deeper draft (accept 138 -> 217). 2026-07-04.
          max = 6;
          min = 1;
          pMin = 0.5;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Sampler nudge — see docs/llm-proxy-usage.md.
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
          # THINKING OFF by default — this is the fast tier. Single-quoted so
          # systemd keeps the inner double quotes (see the same flag's history
          # in git: unquoted, llama.cpp gets invalid JSON). `reasoning` above
          # only bounds and parses thinking for a caller that opts back in per
          # request with chat_template_kwargs.enable_thinking = true; on its
          # own it does not stop the model reasoning.
          "--chat-template-kwargs"
          "'{\"enable_thinking\":false}'"
          "--rope-scaling"
          "yarn"
          "--rope-scale"
          "2"
          "--yarn-orig-ctx"
          "262144"
          # llama-server caps every slot at the GGUF's declared training
          # context ("exceeds the training context of the model - capping"),
          # whatever the rope flags say, so declare the extended window too.
          "--override-kv"
          "qwen35moe.context_length=int:524288"
        ];
      };
    };
  };
}
