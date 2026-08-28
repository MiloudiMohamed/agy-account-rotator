// Package agyplugin embeds the optional Antigravity-native awareness bundle
// (a plugin directory with a skill) that can be installed into
// ~/.gemini/config/plugins/ so the agy agent knows about this tool.
package agyplugin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:bundle
var bundle embed.FS

// Root is where agy discovers global plugins.
func Root() string {
	if p := os.Getenv("AGY_CONFIG_DIR"); p != "" {
		return filepath.Join(p, "plugins")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config", "plugins")
}

// Dest returns the target directory of the bundled plugin.
func Dest() string { return filepath.Join(Root(), "agy-rotator") }

// Install writes the embedded bundle into agy's global plugins root.
func Install() (int, error) {
	n := 0
	err := fs.WalkDir(bundle, "bundle", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, "bundle")
		target := filepath.Join(Dest(), rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := bundle.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, "plugin.json") {
			mode = 0o644
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, nil
}

// Uninstall removes the installed bundle.
func Uninstall() error {
	return os.RemoveAll(Dest())
}

// Installed reports whether the bundle is present.
func Installed() bool {
	_, err := os.Stat(filepath.Join(Dest(), "plugin.json"))
	return err == nil
}

// String renders a one-line status.
func String() string {
	if Installed() {
		return fmt.Sprintf("installed at %s", Dest())
	}
	return "not installed"
}
