# NixFleet Secrets Configuration
# Run `nixfleet secrets rekey` after modifying this file
let
  # Admin keys (for local decryption/rekey)
  admin = "age1cdgl0uys9l7ek32uc8tvwncn2gypdzyl6s7tflgcxdnygnsvcewswsu9nf";

  # Host keys (derived from SSH host keys via ssh-to-age)
  gtr = "age19urtl9njmlx090qmqtjsky7ddv5ulzqzffkkqsetuu7prewandcqyhu0u5";
  gti = "age1zkz4m2md3hnf9ahptl9q8tuu6yqkuv4xcvk7jnyprfuh9rfz2qcq7yzc9y";

  # Host groups
  linuxHosts = [
    gtr
    gti
  ];
  allHosts = linuxHosts;
in
{
  # SMB credentials for personal drives
  "smb-ztaylor.age".publicKeys = [ admin ] ++ linuxHosts;

  # Future secrets can be added here:
  # "api-key.age".publicKeys = [ admin ] ++ allHosts;
}
