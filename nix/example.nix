{ ... }:
{
  services.inkflow = {
    enable = true;
    vaultDir = "/home/anton/obsidian/Anton";
    stateDir = "/var/lib/inkflow";
    environmentFiles = [ "/run/keys/inkflow.env" ];

    gemini = {
      apiKeyFile = "/run/secrets/gemini-api-key";
      model = "gemini-2.5-flash";
    };

    routes = [
      {
        from = "Syncs/";
        pdf_dir = "_files/Attachments/Boox/Syncs";
        note_dir = "03. Resources/Wallet/Syncs";
        note_name = "{stem}.md";
        pdf_name = "{stem}.pdf";
        template = "sync";
        ai = true;
      }
      {
        from = "Meetings/";
        pdf_dir = "_files/Attachments/Boox/Meetings";
        note_dir = "03. Resources/Wallet/Meetings";
        note_name = "{stem}.md";
        pdf_name = "{stem}.pdf";
        template = "meeting";
        ai = true;
      }
      {
        from = "1-1/";
        pdf_dir = "_files/Attachments/Boox/1-1";
        note_dir = "03. Resources/Wallet/1-1";
        note_name = "{stem}.md";
        pdf_name = "{stem}.pdf";
        template = "meeting";
      }
    ];
  };
}
