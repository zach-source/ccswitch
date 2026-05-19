{
  description = "Multi-account switcher and usage monitor for Claude Code";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        version = "2.0.0";
      in
      {
        packages = {
          default = self.packages.${system}.ccswitch;

          # ccswitch is a pure-Go CLI as of v2.0.0 (rewritten from the v1.x
          # bash script). Runtime tools it shells out to — `claude`,
          # `ccusage`, `op` — are resolved from the user's PATH on demand,
          # so the binary is not wrapped.
          ccswitch = pkgs.buildGoModule {
            pname = "ccswitch";
            inherit version;
            src = self;

            vendorHash = "sha256-Cts3oaKQC8LXFMQrT8IvFYnEmgJBSPc2l1kMN1nmCzM=";

            subPackages = [ "cmd/ccswitch" ];
            ldflags = [
              "-s"
              "-w"
            ];

            meta = with pkgs.lib; {
              description = "Multi-account switcher and usage monitor for Claude Code";
              homepage = "https://github.com/zach-source/ccswitch";
              license = licenses.mit;
              mainProgram = "ccswitch";
              platforms = platforms.unix;
            };
          };
        };

        # Allow `nix run`
        apps.default = {
          type = "app";
          program = "${self.packages.${system}.ccswitch}/bin/ccswitch";
        };

        # `nix develop` — toolchain for `make conformance` (Go + bats).
        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.bats
          ];
        };
      }
    )
    // {
      # Overlay for use in other flakes
      overlays.default = final: prev: {
        ccswitch = self.packages.${final.system}.ccswitch;
      };
    };
}
