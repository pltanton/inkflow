{
  description = "inkflow";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system (import nixpkgs { inherit system; }));
    in {
      packages = forAllSystems (system: pkgs: {
        default = pkgs.callPackage ./nix/package.nix { };
        inkflow = pkgs.callPackage ./nix/package.nix { };
      });

      apps = forAllSystems (system: pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${system}.inkflow}/bin/inkflow";
        };
      });

      nixosModules.default = import ./nix/inkflow.nix;

      devShells = forAllSystems (system: pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go ];
        };
      });
    };
}
