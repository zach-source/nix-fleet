"""Apply upstream FlashInfer #4732 before vLLM imports its CUDA kernels."""

import hashlib
import os
import subprocess
from pathlib import Path

ROOT = Path("/usr/local/lib/python3.12/dist-packages/flashinfer/data")
PATCH = Path("/opt/dspark-patches/flashinfer-4732.patch")
CACHE = "/cache/huggingface/flashinfer-4732-d2090811"
FILES = {
    "include/flashinfer/attention/sparse_mla_sm120/prefill_kernel.cuh": (
        "d6ac1a639c0129d980283765a0619be9358d934d894c3cc5625cd96ecaa665ba",
        "7cc5cc114813635d4b1050e4954a0e303aad2d61ef5ecc3ee12b560b0a4c05bc",
    ),
    "include/flashinfer/attention/sparse_mla_sm120/arch/barrier.cuh": (
        "ee7a09bcca526d626c91db5fd3765847ec76c1b52801835cff2a5ae9ef5b2c96",
        "015f9507382fd2a6ad244078574c0f56f692c827619c879b44cfcb1833787615",
    ),
}


def apply():
    # Header-only changes can reuse stale ninja objects; isolate the fixed build.
    if os.environ.get("FLASHINFER_WORKSPACE_BASE") != CACHE:
        raise RuntimeError("sparse MLA fix requires its isolated kernel cache")
    digests = {
        name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in FILES
    }
    if digests == {name: after for name, (_, after) in FILES.items()}:
        return
    if digests != {name: before for name, (before, _) in FILES.items()}:
        raise RuntimeError("sparse MLA headers differ from the pinned image")
    if (
        hashlib.sha256(PATCH.read_bytes()).hexdigest()
        != "120291589fa3304c3a2beb78d6d65145739ef0ea254a54daf0d2997331f98f36"
    ):
        raise RuntimeError("sparse MLA upstream patch changed")
    subprocess.run(
        ["patch", "--batch", "--forward", "--fuzz=0", "-p1", "-i", str(PATCH)],
        cwd=ROOT,
        check=True,
    )
    for name, (_, expected) in FILES.items():
        if hashlib.sha256((ROOT / name).read_bytes()).hexdigest() != expected:
            raise RuntimeError("sparse MLA patched header verification failed")


if __name__ == "__main__":
    apply()
    print("[fabriek-sparse-mla-4732] both headers verified")
