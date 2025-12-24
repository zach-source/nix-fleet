# NixFleet CLI package
{
  lib,
  buildGoModule,
  installShellFiles,
}:

buildGoModule rec {
  pname = "nixfleet";
  version = "0.1.0";

  src = ../../cmd/nixfleet;

  vendorHash = null; # Will be set once we have go.mod

  nativeBuildInputs = [ installShellFiles ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
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
