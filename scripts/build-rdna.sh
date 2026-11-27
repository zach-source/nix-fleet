#!/bin/bash
# Build llama.cpp + rdna-boosts for gfx1151 against /opt/rocm-10.1.
# Output: /opt/llama-rocm-rdna (same flat layout as /opt/llama-rocm-master).
set -euo pipefail
TAG=${TAG:-v18-919794500-r366}
BASE=919794500
W=/opt/build/rdna-boosts
R=/opt/rocm-10.1
OUT=/opt/llama-rocm-rdna
mkdir -p $W && cd $W

[ -d boosts ] || git clone -q https://github.com/zach-source/llama-cpp-rdna-boosts boosts
git -C boosts fetch -q --tags origin
git -C boosts checkout -q "$TAG"

[ -d llama.cpp ] || git clone -q --filter=blob:none https://github.com/ggml-org/llama.cpp
cd llama.cpp
git fetch -q origin
git checkout -q -f "$BASE"
git branch -D rdna-boosts 2>/dev/null || true
export GIT_AUTHOR_NAME=build GIT_AUTHOR_EMAIL=build@localhost GIT_COMMITTER_NAME=build GIT_COMMITTER_EMAIL=build@localhost
git am --abort 2>/dev/null || true
bash ../boosts/scripts/apply-all.sh . </dev/null
git log --oneline -1

export PATH=$R/bin:$R/lib/llvm/bin:$PATH
export LD_LIBRARY_PATH=$R/lib:$R/lib/rocm_sysdeps/lib:$R/lib/llvm/lib:$R/lib/host-math/lib
HIPCXX="$R/lib/llvm/bin/clang" HIP_PATH="$R" ROCM_PATH="$R" \
cmake -S . -B build-gfx1151 -G "Unix Makefiles" \
  -DGGML_HIP=ON -DAMDGPU_TARGETS=gfx1151 -DGGML_NATIVE=ON \
  -DGGML_RPC=ON -DLLAMA_CURL=OFF \
  -DCMAKE_PREFIX_PATH="$R" \
  -DCMAKE_BUILD_TYPE=Release -DLLAMA_BUILD_TESTS=OFF
cmake --build build-gfx1151 --config Release -j"$(nproc)" \
  --target llama-server llama-bench llama-perplexity

sudo rm -rf $OUT.new && sudo mkdir -p $OUT.new
sudo cp -P build-gfx1151/bin/llama-server build-gfx1151/bin/llama-bench build-gfx1151/bin/llama-perplexity $OUT.new/
find build-gfx1151 -name '*.so*' -exec sudo cp -P {} $OUT.new/ \;
echo "llama.cpp $BASE + rdna-boosts $TAG (zach-source fork), HIP gfx1151, TheRock 10.1.0 at $R. Built $(date -u +%F) by build-rdna.sh in $W." | sudo tee $OUT.new/BUILD-INFO >/dev/null
sudo rm -rf $OUT && sudo mv $OUT.new $OUT
ls $OUT
echo BUILD-OK
