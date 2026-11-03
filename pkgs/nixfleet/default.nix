# NixFleet CLI package
{
  lib,
  buildGoModule,
  go,
  installShellFiles,
  gitCommit ? "",
  gitTag ? "",
}:

assert lib.assertMsg (lib.versionAtLeast go.version "1.26.10")
  "nixfleet needs Go >= 1.26.10 (GO-2026-6809), but nixpkgs provides ${go.version}. Update the nixpkgs pin.";

buildGoModule rec {
  pname = "nixfleet";
  version = "0.1.5";

  src = ../../cmd/nixfleet;

  vendorHash = "sha256-JB3QboVypcF0wMFtIP3JJTxbpSeX82F2tWy+n9avQpU=";

  nativeBuildInputs = [ installShellFiles ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.gitCommit=${gitCommit}"
    "-X main.gitTag=${gitTag}"
  ];

  postInstall = ''
    # Generate shell completions once CLI is implemented
    # installShellCompletion --cmd nixfleet \
    #   --bash <($out/bin/nixfleet completion bash) \
    #   --zsh <($out/bin/nixfleet completion zsh) \
    #   --fish <($out/bin/nixfleet completion fish)
  '';

  meta = with lib; {
    description = "Agentless fleet management with Nix";
    homepage = "https://github.com/zach-source/nix-fleet";
    license = licenses.mit;
    maintainers = [ ];
    mainProgram = "nixfleet";
  };
}
