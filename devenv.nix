{
  pkgs,
  lib,
  inputs,
  ...
}:
let
  # CI pins; TestDevenvPinsMatchCI compares them with go.mod and ci.yml.
  goVersion = "1.26";
  golangciLintVersion = "2.12.2";
  lintPkgs = import inputs.nixpkgs-golangci-lint { inherit (pkgs.stdenv) system; };
in
{
  # The asserts must stay inside packages: a top-level assert recurses.
  packages =
    assert lib.assertMsg (
      lib.versions.majorMinor pkgs.go.version == goVersion
    ) "devenv go is ${pkgs.go.version}, CI pins ${goVersion}. Update devenv.yaml.";
    assert lib.assertMsg (lib.versionAtLeast pkgs.go.version "1.26.10")
      "devenv go is ${pkgs.go.version}; GO-2026-6809 needs >= 1.26.10. Update devenv.yaml.";
    assert lib.assertMsg (lintPkgs.golangci-lint.version == golangciLintVersion)
      "devenv golangci-lint is ${lintPkgs.golangci-lint.version}, ci.yml pins v${golangciLintVersion}. Update devenv.yaml.";
    [
      pkgs.go
      pkgs.gopls
      lintPkgs.golangci-lint
      pkgs.delve
      pkgs.nix-prefetch-git
      pkgs.nix-tree
      pkgs.nixfmt
      pkgs.openssh
      pkgs.jq
      pkgs.yq-go
      pkgs.age
      pkgs.ssh-to-age
      pkgs.sops
    ];
}
