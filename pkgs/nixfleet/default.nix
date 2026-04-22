# NixFleet CLI package
{
  lib,
  buildGoModule,
  installShellFiles,
  gitCommit ? "",
  gitTag ? "",
}:

buildGoModule rec {
  pname = "nixfleet";
  version = "0.1.0";

  src = ../../cmd/nixfleet;

  vendorHash = "sha256-bMpgBgpnO6rMoXW0IQouJwBed8sfVHLx7s0ThlvmJSo=";

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
    homepage = "https://github.com/your-org/nixfleet";
    license = licenses.mit;
    maintainers = [ ];
    mainProgram = "nixfleet";
  };
}
