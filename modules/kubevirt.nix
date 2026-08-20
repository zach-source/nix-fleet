# NixFleet KubeVirt node module
#
# KubeVirt hardcodes the kubelet root as /var/lib/kubelet (a compile-time
# constant, pkg/util/util.go). k0s runs kubelet with
# --root-dir=/var/lib/k0s/kubelet, so virt-handler writes each VM's
# container-disk binary into /var/lib/kubelet/pods/<uid>/volumes/... — a
# directory the kubelet never populates. The virt-launcher init container then
# dies with:
#
#   exec: "/container-disk-binary/usr/bin/container-disk": no such file or
#   directory
#
# and the VM crash-loops forever. Bind the real pods tree into the path
# KubeVirt expects.
#
# Only `pods` is bound, NOT the whole directory: /var/lib/kubelet/device-plugins
# is genuinely in use — kubelet's DevicePluginPath is itself a hardcoded
# /var/lib/kubelet/device-plugins constant, independent of --root-dir — and
# shadowing it would break the kvm/tun/vhost-net device plugins.
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.kubevirt;
in
{
  options.nixfleet.modules.kubevirt = {
    enable = lib.mkEnableOption "KubeVirt node prerequisites (kubelet root bind-mount)";

    kubeletRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/k0s/kubelet";
      description = ''
        The kubelet's actual --root-dir. Bound onto /var/lib/kubelet, which
        KubeVirt hardcodes.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.systemd.units = {
      "nixfleet-kubevirt-kubelet-bind.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Bind ${cfg.kubeletRoot}/pods onto /var/lib/kubelet/pods for KubeVirt
          # MUST run BEFORE k0s. virt-handler mounts the host's
          # /var/lib/kubelet/pods into its own container at pod-create time with
          # propagation None, so it captures whatever that path resolves to right
          # then and never sees a later bind. If k0s started first it could
          # schedule virt-handler against the empty pre-bind directory, and every
          # VM on the node would fail with "cannot compute checksums as
          # containerdisk/kernelboot containers seem to have been terminated"
          # until the pod was manually deleted.
          Before=k0sworker.service k0scontroller.service
          After=local-fs.target
          RequiredBy=k0sworker.service

          [Service]
          Type=oneshot
          RemainAfterExit=yes
          # Both dirs are created first: on a node that has never run k0s the
          # kubelet root does not exist yet, and binding must still be in place
          # before the kubelet starts populating it.
          ExecStart=/bin/sh -c 'mkdir -p ${cfg.kubeletRoot}/pods /var/lib/kubelet/pods; mountpoint -q /var/lib/kubelet/pods && exit 0; mount --rbind ${cfg.kubeletRoot}/pods /var/lib/kubelet/pods && mount --make-rshared /var/lib/kubelet/pods'
          # ponytail: unmount on stop is deliberately omitted — tearing the bind
          # down under running virt-launchers is worse than leaving it.

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      nixfleet-kubevirt-kubelet-bind = {
        type = "command";
        command = "mountpoint -q /var/lib/kubelet/pods";
        timeout = 5;
      };
    };
  };
}
