package config

type Config struct {
	ListenAddr string `toml:"listen_addr" json:"listen_addr"`
	WebDAVUser string `toml:"webdav_user" json:"webdav_user"`
	WebDAVPass string `toml:"webdav_pass" json:"webdav_pass"`

	TemplateDir    string `toml:"template_dir" json:"template_dir"`
	VaultDir       string `toml:"vault_dir" json:"vault_dir"`
	DefaultPDFDir  string `toml:"default_pdf_dir" json:"default_pdf_dir"`
	DefaultNoteDir string `toml:"default_note_dir" json:"default_note_dir"`
	StateFile      string `toml:"state_file" json:"state_file"`

	Routes []Route `toml:"route" json:"route"`
}

type Route struct {
	From     string `toml:"from" json:"from"`
	PDFDir   string `toml:"pdf_dir" json:"pdf_dir"`
	NoteDir  string `toml:"note_dir" json:"note_dir"`
	NoteName string `toml:"note_name" json:"note_name"`
	PDFName  string `toml:"pdf_name" json:"pdf_name"`
	Template string `toml:"template" json:"template"`
}
