# NixFleet library functions
{ lib }:

{
  # Define a NixFleet host configuration
  mkHost =
    {
      name,
      base, # "ubuntu" or "nixos"
      addr,
      sshUser ? "deploy",
      roles ? [ ],
      osUpdates ? { },
      rollout ? { },
      extraConfig ? { },
    }:
    {
      inherit
        name
        base
        addr
        sshUser
        roles
        extraConfig
        ;

      osUpdates = lib.recursiveUpdate (
        if base == "ubuntu" then
          {
            ubuntu = {
              mode = "manual";
              autoReboot = false;
              rebootWindow = null;
              holds = [ ];
              maxConcurrentReboots = 1;
            };
          }
        else
          {
            nixos = {
              autoSwitch = true;
            };
          }
      ) osUpdates;

      rollout = {
        canaryPercent = rollout.canaryPercent or 10;
        maxParallel = rollout.maxParallel or 5;
        pauseBetweenBatches = rollout.pauseBetweenBatches or 30;
      }
      // rollout;
    };

  # Parse reboot window string (e.g., "Sun 02:00-04:00")
  parseRebootWindow =
    windowStr:
    if windowStr == null then
      null
    else
      let
        parts = lib.splitString " " windowStr;
        day = builtins.elemAt parts 0;
        timeRange = builtins.elemAt parts 1;
        times = lib.splitString "-" timeRange;
      in
      {
        inherit day;
        startTime = builtins.elemAt times 0;
        endTime = builtins.elemAt times 1;
      };

  # Group hosts by attribute
  groupHostsBy =
    attr: hosts: lib.groupBy (host: toString (host.${attr} or "default")) (lib.attrValues hosts);

  # Filter hosts by base OS
  filterByBase = base: hosts: lib.filterAttrs (name: host: host.base == base) hosts;

  # Get hosts that need reboot
  hostsNeedingReboot = hosts: lib.filterAttrs (name: host: host.state.rebootNeeded or false) hosts;
}
