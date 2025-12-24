{
  description = "NixFleet - Agentless fleet management with Nix";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    nix-darwin = {
      url = "github:LnL7/nix-darwin";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      nix-darwin,
    }:
    let
      # Supported systems for the CLI/tooling
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      # Helper to generate outputs for each system
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;

      # Nixpkgs instantiated for each system
      nixpkgsFor = forAllSystems (
        system:
        import nixpkgs {
          inherit system;
          overlays = [ self.overlays.default ];
        }
      );

      # Create a NixFleet host configuration (for Ubuntu hosts)
      mkNixFleetConfiguration =
        {
          system ? "x86_64-linux",
          modules ? [ ],
          specialArgs ? { },
        }:
        let
          pkgs = nixpkgsFor.${system};
          lib = nixpkgs.lib;

          # Evaluate the modules
          evaluated = lib.evalModules {
            modules = [
              # Core NixFleet module
              ./modules/nixfleet

              # Provide pkgs
              { _module.args = { inherit pkgs; }; }
            ]
            ++ modules;
            specialArgs = {
              inherit lib;
            }
            // specialArgs;
          };
        in
        {
          inherit (evaluated) config options;

          # The main system derivation for deployment
          system = evaluated.config.nixfleet.ubuntu.system;

          # Convenience accessors
          manifestHash = evaluated.config.nixfleet.ubuntu.manifestHash;
          hostName = evaluated.config.nixfleet.host.name;
          base = evaluated.config.nixfleet.host.base;
        };

      # Create a NixOS configuration with NixFleet modules
      mkNixOSFleetConfiguration =
        {
          system ? "x86_64-linux",
          modules ? [ ],
          specialArgs ? { },
        }:
        nixpkgs.lib.nixosSystem {
          inherit system specialArgs;
          modules = [
            # NixFleet options module (for nixfleet.* interface)
            ./modules/nixfleet/options.nix

            # NixOS backend compiler (translates nixfleet.* to NixOS options)
            ./backends/nixos/compile.nix
          ]
          ++ modules;
        };

      # Create a nix-darwin configuration with NixFleet modules
      mkDarwinFleetConfiguration =
        {
          system ? "aarch64-darwin",
          modules ? [ ],
          specialArgs ? { },
        }:
        nix-darwin.lib.darwinSystem {
          inherit system specialArgs;
          modules = [
            # NixFleet options module (for nixfleet.* interface)
            ./modules/nixfleet/options.nix

            # nix-darwin backend compiler (translates nixfleet.* to darwin options)
            ./backends/darwin/compile.nix
          ]
          ++ modules;
        };

    in
    {
      # Overlay for NixFleet packages
      overlays.default = final: prev: {
        nixfleet = final.callPackage ./pkgs/nixfleet { };
      };

      # CLI package for each system
      packages = forAllSystems (system: {
        default = nixpkgsFor.${system}.nixfleet;
        nixfleet = nixpkgsFor.${system}.nixfleet;
      });

      # Development shell
      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgsFor.${system};
        in
        {
          default = pkgs.mkShell {
            name = "nixfleet-dev";
            packages = with pkgs; [
              # Go toolchain (for CLI)
              go
              gopls
              golangci-lint
              delve

              # Nix tooling
              nix-prefetch-git
              nix-tree
              nixpkgs-fmt

              # SSH/deployment testing
              openssh

              # General utilities
              jq
              yq-go
              age
              ssh-to-age
              sops
            ];

            shellHook = ''
              echo "NixFleet development shell"
              echo "Go version: $(go version)"
            '';
          };
        }
      );

      # NixFleet library functions for host definitions
      lib = (import ./lib { inherit (nixpkgs) lib; }) // {
        inherit mkNixFleetConfiguration mkNixOSFleetConfiguration mkDarwinFleetConfiguration;
      };

      # NixFleet library functions are available via self.lib
      # Users can create their own configurations using:
      #   nixfleetConfigurations.myhost = self.lib.mkNixFleetConfiguration { ... };
      #   nixosConfigurations.myhost = self.lib.mkNixOSFleetConfiguration { ... };
      #   darwinConfigurations.myhost = self.lib.mkDarwinFleetConfiguration { ... };
    };
}
