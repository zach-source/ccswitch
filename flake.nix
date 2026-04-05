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
        version = "1.0.0";
      in
      {
        packages = {
          default = self.packages.${system}.ccswitch;

          ccswitch = pkgs.stdenv.mkDerivation {
            pname = "ccswitch";
            inherit version;
            src = self;

            nativeBuildInputs = [ pkgs.makeWrapper ];

            dontBuild = true;

            installPhase = ''
              runHook preInstall
              install -Dm755 ccswitch.sh $out/bin/ccswitch
              wrapProgram $out/bin/ccswitch \
                --prefix PATH : ${
                  pkgs.lib.makeBinPath [
                    pkgs.bash
                    pkgs.coreutils
                    pkgs.curl
                    pkgs.jq
                    pkgs.python3
                  ]
                }
              runHook postInstall
            '';

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
      }
    )
    // {
      # Overlay for use in other flakes
      overlays.default = final: prev: {
        ccswitch = self.packages.${final.system}.ccswitch;
      };
    };
}
