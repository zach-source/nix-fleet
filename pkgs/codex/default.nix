# OpenAI Codex CLI — official prebuilt release binary.
#
# nixpkgs has `codex`, but it lags (0.139.0 at our pin) and builds from source
# with cargo + pnpm, which would mean a rust toolchain and a cargoHash bump on
# every version. Upstream publishes static musl / darwin binaries per release,
# so unpacking those gets the latest version for the cost of a hash.
#
# ponytail: bump by changing `version` + the four hashes; `nix-prefetch-url`
# or `nix hash file --sri` on the asset. No deps to re-vendor.
{
  lib,
  stdenvNoCC,
  fetchurl,
}:

let
  version = "0.160.0";

  # nix system -> (release target, sha256) for codex-<target>.tar.gz
  targets = {
    x86_64-linux = {
      target = "x86_64-unknown-linux-musl";
      hash = "sha256-MGhlQX1O56kneFhSkQpSf0Hh4Vmt05CsWuOsy2fUShM=";
    };
    aarch64-linux = {
      target = "aarch64-unknown-linux-musl";
      hash = "sha256-iDYgE5kl9nfloSyVuoeh1/QhOI67eXscEKukKgTt6dc=";
    };
    aarch64-darwin = {
      target = "aarch64-apple-darwin";
      hash = "sha256-B8PHyjdqj3kRFTQvUxON2jfpfPopuBJdBlLZN4SJS10=";
    };
    x86_64-darwin = {
      target = "x86_64-apple-darwin";
      hash = "sha256-pQwQYG5OgbjdL3trq2NVlaq8dzyEzyBxdz+68GeCX78=";
    };
  };
in

stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "codex";
  inherit version;

  src =
    let
      me =
        targets.${stdenvNoCC.hostPlatform.system}
          or (throw "codex: no prebuilt release for ${stdenvNoCC.hostPlatform.system}");
    in
    fetchurl {
      url = "https://github.com/openai/codex/releases/download/rust-v${version}/codex-${me.target}.tar.gz";
      inherit (me) hash;
    };

  # The tarball is a single bare binary named after the target triple.
  sourceRoot = ".";

  installPhase = ''
    runHook preInstall
    install -Dm755 codex-* $out/bin/codex
    runHook postInstall
  '';

  # Linux build is static musl; darwin needs no patching either.
  dontPatchELF = true;
  dontStrip = true;

  doInstallCheck = true;
  installCheckPhase = ''
    $out/bin/codex --version | grep -q '${version}'
  '';

  meta = {
    description = "OpenAI Codex CLI — coding agent that runs locally";
    homepage = "https://github.com/openai/codex";
    license = lib.licenses.asl20;
    mainProgram = "codex";
    platforms = lib.attrNames targets;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
  };
})
