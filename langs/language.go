package langs

// Language is one entry in the languages JSON file referenced by
// DREDD_LANGUAGES_FILE.
//
// Rootfs is a path *relative to* DREDD_ROOTFS_DIR (or an absolute path).
// SourceFile, CompileCmd and RunCmd are passed verbatim to the guest agent.
type Language struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Rootfs     string `json:"rootfs"`
	SourceFile string `json:"source_file"`
	CompileCmd string `json:"compile_cmd"`
	RunCmd     string `json:"run_cmd"`
}

// PublicView is what /languages returns. It deliberately omits paths and
// commands so we don't leak host layout to API callers.
type PublicView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (l Language) Public() PublicView {
	return PublicView{ID: l.ID, Name: l.Name, Version: l.Version}
}
