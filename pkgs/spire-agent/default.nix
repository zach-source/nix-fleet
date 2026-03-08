# SPIRE Agent — local agent for connecting to the fleet SPIRE server
{ lib, buildGoModule, fetchFromGitHub }:

buildGoModule rec {
  pname = "spire-agent";
  version = "1.9.6";

  src = fetchFromGitHub {
    owner = "spiffe";
    repo = "spire";
    rev = "v${version}";
    hash = "sha256-wubrZJBPLA83VB57UVKLuh2cmyXHouwN4BVPiHFl+1s=";
  };

  vendorHash = "sha256-tx0zIr9rXuOvt+77Sp6dIdtN21fDX5FdnTxGpHWo7+A=";

  subPackages = [ "cmd/spire-agent" ];

  ldflags = [
    "-s"
    "-w"
    "-X github.com/spiffe/spire/pkg/common/version.gittag=v${version}"
  ];

  # Skip tests — they require a running SPIRE server
  doCheck = false;

  meta = with lib; {
    description = "SPIFFE Runtime Environment Agent";
    homepage = "https://github.com/spiffe/spire";
    license = licenses.asl20;
    mainProgram = "spire-agent";
  };
}
