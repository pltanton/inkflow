{config, lib, pkgs, ...}:
let
  cfg = config.services.inkflow;
  toml = pkgs.formats.toml {};

  routeType = lib.types.submodule {
    options = {
      from = lib.mkOption {type = lib.types.str;};
      pdf_dir = lib.mkOption {type = lib.types.str; default = "";};
      note_dir = lib.mkOption {type = lib.types.str; default = "";};
      note_name = lib.mkOption {type = lib.types.str; default = "";};
      pdf_name = lib.mkOption {type = lib.types.str; default = "";};
      template = lib.mkOption {type = lib.types.str; default = "";};
    };
  };

  mkRoute = r:
    lib.filterAttrs (_: v: v != "" && v != null) {
      inherit (r)
        from
        pdf_dir
        note_dir
        note_name
        pdf_name
        template;
    };

  mkConfig = attrs: lib.filterAttrs (_: v: v != "" && v != null) attrs;

  baseSettings = mkConfig {
    listen_addr = cfg.listenAddr;
    template_dir = cfg.templateDir;
    webdav_user = cfg.webdavUser;
    webdav_pass = cfg.webdavPass;
    vault_dir = cfg.vaultDir;
    default_pdf_dir = cfg.defaultPdfDir;
    default_note_dir = cfg.defaultNoteDir;
    state_file = cfg.stateFile;
    route = map mkRoute cfg.routes;
  };

  configFile = toml.generate "inkflow.toml" (baseSettings // cfg.extraSettings);
in {
  options.services.inkflow = {
    enable = lib.mkEnableOption "inkflow service";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./package.nix {};
      defaultText = lib.literalExpression "pkgs.callPackage ./nix/package.nix {}";
      description = "inkflow package to run";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "inkflow";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "inkflow";
    };

    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/inkflow";
    };

    stateFile = lib.mkOption {
      type = lib.types.str;
      default = "${cfg.stateDir}/state.db";
    };

    listenAddr = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:8080";
    };

    templateDir = lib.mkOption {
      type = lib.types.str;
      default = "";
    };

    webdavUser = lib.mkOption {
      type = lib.types.str;
      default = "";
    };

    webdavPass = lib.mkOption {
      type = lib.types.str;
      default = "";
    };

    vaultDir = lib.mkOption {
      type = lib.types.str;
      default = "";
    };

    defaultPdfDir = lib.mkOption {
      type = lib.types.str;
      default = "Attachments/Boox";
    };

    defaultNoteDir = lib.mkOption {
      type = lib.types.str;
      default = "00 Inbox";
    };

    routes = lib.mkOption {
      type = lib.types.listOf routeType;
      default = [];
    };

    extraSettings = lib.mkOption {
      type = lib.types.attrs;
      default = {};
      description = "Extra TOML settings merged into inkflow config";
    };

    environmentFiles = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "systemd environment files for secrets like webdav credentials";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.${cfg.user} = lib.mkIf (cfg.user == "inkflow") {
      isSystemUser = true;
      group = cfg.group;
      home = cfg.stateDir;
    };

    users.groups.${cfg.group} = {};

    systemd.tmpfiles.rules = [
      "d ${cfg.stateDir} 0750 ${cfg.user} ${cfg.group} -"
    ];

    systemd.services.inkflow = {
      description = "Inkflow service";
      wantedBy = ["multi-user.target"];

      serviceConfig = {
        ExecStart = "${cfg.package}/bin/inkflow --config ${configFile} serve";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.stateDir;
        EnvironmentFile = cfg.environmentFiles;
        UMask = "0002";
        Restart = "always";
        RestartSec = "5s";
        NoNewPrivileges = true;
      };
    };
  };
}
