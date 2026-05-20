package langs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Registry struct {
	byID  map[string]Language
	order []string
}

// Load reads the languages file at path and resolves each Rootfs against
// rootfsDir if it is not already absolute.
func Load(path, rootfsDir string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read languages file: %w", err)
	}
	var list []Language
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse languages file: %w", err)
	}
	r := &Registry{byID: make(map[string]Language, len(list))}
	for _, l := range list {
		if l.ID == "" || l.Rootfs == "" || l.RunCmd == "" || l.SourceFile == "" {
			return nil, fmt.Errorf("language entry missing required fields: %+v", l)
		}
		if !filepath.IsAbs(l.Rootfs) {
			l.Rootfs = filepath.Join(rootfsDir, l.Rootfs)
		}
		if _, dup := r.byID[l.ID]; dup {
			return nil, fmt.Errorf("duplicate language id %q", l.ID)
		}
		r.byID[l.ID] = l
		r.order = append(r.order, l.ID)
	}
	return r, nil
}

func (r *Registry) Get(id string) (Language, bool) {
	l, ok := r.byID[id]
	return l, ok
}

func (r *Registry) All() []Language {
	out := make([]Language, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

func (r *Registry) PublicAll() []PublicView {
	out := make([]PublicView, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id].Public())
	}
	return out
}
