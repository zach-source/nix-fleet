# DeepSeek-V4-Flash across a stacked DGX Spark pair — shared definition.
#
# Both Sparks run a byte-identical vLLM command except for --node-rank, so the
# flag set lives here once and each host file imports it and states its rank.
# Rank divergence is the classic way a TP job hangs in NCCL rendezvous with no
# useful error, so it is worth the indirection.
#
# Provenance: MiaAI-Lab dual-DGX-Spark recipe, benchmarked at 1,630.5 tok/s
# prefill (pp=2048, tg=128, depth=0, runs=5) on vLLM
# 0.21.1rc1.dev339+g1967a5627bc3, model revision 60d8d707.
#
# Worth knowing: DeepSeek V4-Flash was tried on the AMD gfx1151 boxes and was
# un-runnable there — the dsv4 ops are CUDA/Metal only (see hosts/gtr-153.nix).
# That is precisely why it lands on the Sparks.
#
# NOT packaged by NixFleet: `dsv4-vllm-entrypoint` ships with the vendor recipe,
# and the vLLM build is a git-pinned dev wheel (dev339+g1967a5627bc3). Both must
# already exist on the box; entrypointPath below points at them.
{
  nodeRank,
  # Rank 0's address on the ConnectX-7 fabric — NOT its management IP. NCCL and
  # the rendezvous both ride the 200GbE link. Matches nodeIndex 10 in
  # modules/dgx-spark-cluster.nix.
  masterAddr ? "192.168.100.10",
  entrypointPath ? "/opt/dsv4/bin/dsv4-vllm-entrypoint",
  # The CX7 interface NCCL should use. Left unset, NCCL picks by its own
  # heuristics and may choose the 10GbE management NIC — the job still runs,
  # just at a fraction of the bandwidth, which is miserable to diagnose.
  #
  # The LEFT port is the cabled one on this pair (verified on spark-5267), so
  # this must stay in step with modules.dgxSparkCluster.interfaces in both host
  # files. Pointing NCCL at a down interface is the silent-hang case.
  ncclInterface ? "enp1s0f0np0",
}:

{
  nixfleet.modules.vllm = {
    enable = true;
    services.dsv4-flash = {
      description = "DeepSeek-V4-Flash — TP=2 across stacked DGX Sparks, 1M ctx, FP8 KV";
      entrypoint = entrypointPath;
      model = "deepseek-ai/DeepSeek-V4-Flash";
      servedModelName = "deepseek-v4-flash";
      port = 8000;

      cluster = {
        enable = true;
        nnodes = 2;
        inherit nodeRank;
        masterAddr = masterAddr;
        masterPort = 25000;
      };

      environment = {
        NCCL_SOCKET_IFNAME = ncclInterface;
      };

      extraFlags = [
        "--trust-remote-code"
        # TP=2 spans the two Sparks; PP=1 because the recipe splits by tensor,
        # not by layer.
        "--tensor-parallel-size"
        "2"
        "--pipeline-parallel-size"
        "1"
        "--kv-cache-dtype"
        "fp8"
        # 256 pairs with the 1M context — a smaller block size blows up the
        # block table at this length.
        "--block-size"
        "256"
        "--max-model-len"
        "1000000"
        # Only 6 concurrent sequences: at 1M context the KV cache, not compute,
        # is the binding constraint.
        "--max-num-seqs"
        "6"
        "--max-num-batched-tokens"
        "8192"
        "--gpu-memory-utilization"
        "0.83"
        "--enable-prefix-caching"
        # Single-quoted so systemd passes the JSON through intact. Unquoted, the
        # braces and quotes are mangled and vLLM rejects the argument.
        "--speculative-config"
        "'{\"method\":\"mtp\",\"num_speculative_tokens\":2}'"
        "--tokenizer-mode"
        "deepseek_v4"
        "--distributed-executor-backend"
        "mp"
        "--tool-call-parser"
        "deepseek_v4"
        "--enable-auto-tool-choice"
        "--reasoning-parser"
        "deepseek_v4"
        "--enable-flashinfer-autotune"
      ];
    };
  };
}
