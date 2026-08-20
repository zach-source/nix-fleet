# NixFleet base package set
#
# The tools every SSH-managed host in the fleet is expected to have. Before
# this existed each host repeated the same list verbatim, which drifted the
# moment anything was added — iperf3 went into the six standard hosts and the
# DGX Spark examples silently fell behind, which is exactly the failure this
# prevents.
#
# `nixfleet.packages` is a listOf package, so the module system concatenates
# this with whatever a host declares itself. Hosts therefore list only their
# EXTRAS (gti adds nfs-utils, for instance) rather than restating the base.
#
# Adding something here adds it fleet-wide. If only one host needs a tool, put
# it in that host's own `packages` instead.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.basePackages;
in
{
  options.nixfleet.modules.basePackages = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Install the fleet-wide base package set. Defaults to true, so importing
        this module is enough — set false on a host that genuinely needs a
        minimal profile.
      '';
    };

    packages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = with pkgs; [
        curl
        git
        htop
        iperf3
        jq
        tmux
        vim
      ];
      description = ''
        The base set itself. Exposed as an option so a host can override it
        wholesale, though adding to the host's own `nixfleet.packages` is
        usually what you want — the two lists merge.

        Note iperf3's nixpkgs attribute is `iperf3` but its pname is `iperf`,
        so it appears in a closure as `iperf-<version>` with no 3 after
        "iperf". The binary is `iperf3`.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.packages = cfg.packages;
  };
}
