# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# The "Qwen3.6" node — both family variants co-hosted:
#   :8084  Qwen3.6-35B-A3B MoE  + classic Qwen3.5-0.8B draft (~48 tok/s)
#                                 (MTP crashes the recurrent MoE — see below)
#   :8085  Qwen3.6-27B    dense + MTP self-speculation (~16 tok/s, pure 3.6)
# Build: /opt/llama-rocm-latest (commit 6a257d4) — fork retired, PR #19493
# natively handles qwen35 spec. 131GB unified VRAM, ROCm 7.13 (TheRock), gfx1151
{ pkgs, ... }:

{
  imports = [
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

    # k0s worker, declaratively managed. system-reserved=78Gi -> ~44Gi k8s
    # allocatable (was an out-of-band 98Gi/24Gi); 78Gi stays for inference.
    k0s.worker.enable = true;

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

    packages = with pkgs; [
      git
      htop
      iperf3
      curl
      jq
      tmux
      vim
    ];

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
        description = "Qwen3.6-35B-A3B MoE + classic draft (MTP unstable on MoE)";
        # Non-MTP GGUF (UD-Q6_K_XL, 29.7GB).
        model = "/srv/models/Qwen3.6-35B-A3B-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        ctxSize = 200000;
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
        # Sampler nudge — see hosts/gtr-152.nix / docs/llm-proxy-usage.md.
        # (qwen36-27b below intentionally KEEPS Qwen's official coding sampling
        # — temp=0.6 top-k=20 — so don't blanket-nudge it.)
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };

      # Qwen3.6-27B DENSE — MOVED to gtr-153 (2026-07-17) to relieve GPU
      # contention here: gtr-151 was pegged at 100% GPU with three big models
      # (35B-A3B MoE + 27B + ornith) sharing one gfx1151, which spiked ornith's
      # latency. The 27B now runs on gtr-153's otherwise-idle GPU
      # (hosts/gtr-153.nix), and ornith's freed headroom goes to a larger
      # context (65K -> 200K) below. See docs/llm-proxy-usage.md.

      # Qwen3.8-27B dense — REPLACES Ornith-1.0-35B-MoE at this port
      # (2026-08-15). Ornith was the fleet's coding-agent model; Qwen3.8 beats
      # it on the agentic/coding evals it was chosen for, and unlike Ornith it
      # is a dense 27B rather than a 35B-A3B MoE, so it sidesteps the qwen35moe
      # MTP "unspecified launch failure" history documented on :8084 above.
      # Ornith's GGUF stays on disk for a one-line revert.
      #
      # Q8_0 (29GB) — the highest practical quant, chosen because gtr-151 has
      # the fleet's most headroom and because gtr-153's Q5_K_XL showed only
      # 52-62% MTP draft acceptance vs the Qwen3.6-27B's 70-84%. This deploy
      # tests both suspected causes at once: higher quant, and a single-repo
      # MTP-merged GGUF instead of gtr-153's cross-repo pairing (unsloth
      # weights + ggml-org draft head).
      #
      # Source is Jackrong/Qwen3.8-27B-MTP-GGUF, whose card claims the MTP head
      # is bundled in ("No additional draft model is required"). Treat that as
      # unverified — the card also reports "0.5B params" and architecture
      # "clip", and its Q8_0 is within 1,696 bytes of unsloth's plain Q8_0
      # while an MTP head is ~3.16GB. If the load log shows no MTP tensors,
      # add `--spec-draft-model /srv/models/mtp-Qwen3.8-27B-Q8_0.gguf` to
      # extraFlags (the gtr-153 pattern).
      services.qwen38-27b = {
        description = "Qwen3.8-27B dense Q8_0 (Jackrong MTP-merged) @ 250K ctx";
        model = "/srv/models/Qwen3.8-27B-MTP-Q8_0.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8086;
        # 250K baseline context, and ctx-size is the TOTAL KV budget split
        # across --parallel slots — so parallel=1 to give a single request the
        # full 250K (ornith ran 524288/2 for two 256K slots). The hybrid
        # Gated-DeltaNet arch keeps this affordable: only 16 of 64 layers are
        # full attention, so 250K of q4_0 KV is ~5G, not the ~20G a dense-
        # attention 27B would need.
        ctxSize = 250000;
        parallel = 1;
        # 2048 (not ornith's 512) because prefill is this model's real cost
        # centre — measured 184 t/s at 50K on gtr-153, i.e. TTFT grows fast
        # with context. Bigger batches buy prefill throughput.
        batchSize = 2048;
        ubatchSize = 2048;
        newCli = true;
        # nMax=2 per Jackrong's card (it benchmarks max-draft 2 at 77.9%
        # acceptance); also matches the fleet's n_max=1-2 precedent.
        mtp = {
          nMax = 2;
        };
        # Kept so that a client which explicitly re-enables thinking still gets
        # it parsed into `reasoning_content` and bounded to 2048 tokens. The
        # default-off switch is `--chat-template-kwargs` in extraFlags below —
        # NOT this. `--reasoning-budget 0` was tried first and does NOT stop
        # generation: the model still produced reasoning, llama.cpp merely
        # split it into the `reasoning_content` field, so the tokens were still
        # paid for. Verified on this endpoint.
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Qwen3.8 non-thinking ("instruct") sampling from the model card —
        # temp 0.7 / top_p 0.80 / presence_penalty 1.5, which differs from the
        # thinking-mode preset (1.0 / 0.95 / 0.0) used on gtr-153:8085.
        # presence_penalty curbs the repetition non-thinking mode is prone to;
        # the card warns values above ~2 cause language mixing.
        extraFlags = [
          # THINKING OFF by default for every caller. Single-quoted so systemd
          # preserves the inner double quotes — unquoted, systemd strips them
          # and llama.cpp receives invalid JSON. A client can still opt back in
          # per-request with chat_template_kwargs.enable_thinking = true.
          "--chat-template-kwargs"
          "'{\"enable_thinking\":false}'"
          "--temp"
          "0.7"
          "--top-p"
          "0.80"
          "--top-k"
          "20"
          "--min-p"
          "0.0"
          "--presence-penalty"
          "1.5"
        ];
      };
    };
  };
}
