{
  lib,
  buildGoModule,
}:
buildGoModule {
  pname = "inkflow";
  version = "0.1.0";
  src = lib.cleanSourceWith {
    src = ../.;
    filter = path: type:
      let
        name = baseNameOf path;
      in
        name != "vendor" && name != "assets";
  };
  vendorHash = "sha256-yauuBw0PWch64YBFHBBNNypuV3LH+yITLEgZtOkmqAY=";
}
