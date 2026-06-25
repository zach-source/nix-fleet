# Declarative k0s worker node management for nixfleet.
#
# Codifies what was previously an imperative `k0s install worker` (binary at
# /usr/local/bin/k0s, the k0sworker.service unit, and the kubelet args). Enabling
# this on an Ubuntu host makes `nixfleet apply` own the worker: it installs the
# pinned k0s binary, writes the systemd unit, and (re)starts it. Changing
# systemReservedMemory and re-applying transparently re-rolls the kubelet with
# the new node-allocatable cap.
#
# Join automation: provide the worker join token at tokenPath (deploy it via
# nixfleet.secrets for a fresh node — generate it on the controller with
# `k0s token create --role=worker`). Already-joined nodes don't need a valid
# token to restart (the kubelet uses its certs under /var/lib/k0s), so changing
# the cap on existing workers is safe.
{ config, lib, ... }:
let
  cfg = config.nixfleet.k0s.worker;
  kubeletExtraArgs = lib.concatStringsSep " " (
    [ "--system-reserved=memory=${cfg.systemReservedMemory}" ] ++ cfg.extraKubeletArgs
  );
in
{
  options.nixfleet.k0s.worker = {
    enable = lib.mkEnableOption "k0s worker node (declaratively managed by nixfleet)";

    version = lib.mkOption {
      type = lib.types.str;
      default = "v1.34.2+k0s.0";
      description = "k0s version installed at /usr/local/bin/k0s (downloaded if missing/mismatched).";
    };

    tokenPath = lib.mkOption {
      type = lib.types.str;
      default = "/etc/k0s/worker-join-token";
      description = "Path to the worker join token (only consulted on the initial join).";
    };

    systemReservedMemory = lib.mkOption {
      type = lib.types.str;
      default = "78Gi";
      example = "78Gi";
      description = ''
        Memory reserved for the host (kubelet --system-reserved). Node k8s
        allocatable = machine memory - this. On the 122 GiB gtr nodes, 78Gi
        leaves ~44 GiB allocatable for pods and keeps 78 GiB for the systemd
        llama inference (which is NOT a k8s workload). Raise it to shrink the
        pod cap, lower it to grow it.
      '';
    };

    extraKubeletArgs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "--max-pods=200" ];
      description = "Additional kubelet args appended after --system-reserved.";
    };
  };

  config = lib.mkIf cfg.enable {
    # Ensure the pinned k0s binary is present (idempotent: only downloads when
    # missing or the wrong version — a no-op on already-provisioned workers).
    nixfleet.hooks.preActivate = ''
      install -d -m 0755 /etc/k0s
      if [ "$(/usr/local/bin/k0s version 2>/dev/null)" != "${cfg.version}" ]; then
        echo "[k0s] installing ${cfg.version}"
        arch="$(uname -m)"
        case "$arch" in x86_64) a=amd64 ;; aarch64) a=arm64 ;; *) a="$arch" ;; esac
        curl -fsSL "https://github.com/k0sproject/k0s/releases/download/${cfg.version}/k0s-${cfg.version}-$a" \
          -o /usr/local/bin/k0s.tmp
        chmod +x /usr/local/bin/k0s.tmp
        mv -f /usr/local/bin/k0s.tmp /usr/local/bin/k0s
      fi
    '';

    # The worker unit (mirrors `k0s install worker`, with the args templated).
    # nixfleet restarts it on text change → re-rolls the kubelet with the new cap.
    nixfleet.systemd.units."k0sworker.service" = {
      enabled = true;
      text = ''
        [Unit]
        Description=k0s - Zero Friction Kubernetes
        Documentation=https://docs.k0sproject.io
        ConditionFileIsExecutable=/usr/local/bin/k0s
        After=network-online.target
        Wants=network-online.target

        [Service]
        StartLimitInterval=5
        StartLimitBurst=10
        ExecStart=/usr/local/bin/k0s worker --kubelet-extra-args="${kubeletExtraArgs}" --token-file=${cfg.tokenPath}
        RestartSec=10
        Delegate=yes
        KillMode=process
        LimitCORE=infinity
        TasksMax=infinity
        TimeoutStartSec=0
        LimitNOFILE=999999
        Restart=always

        [Install]
        WantedBy=multi-user.target
      '';
    };
  };
}
