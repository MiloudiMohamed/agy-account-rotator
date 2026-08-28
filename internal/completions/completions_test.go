package completions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptsContainCommands(t *testing.T) {
	required := []string{"add", "list", "status", "quota", "why", "history", "statusline", "proxy", "rotate", "doctor"}
	for _, cmd := range required {
		if !strings.Contains(ZshScript, cmd) {
			t.Errorf("ZshScript missing command %q", cmd)
		}
		if !strings.Contains(BashScript, cmd) {
			t.Errorf("BashScript missing command %q", cmd)
		}
	}
}

func TestInstallCustomHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	zshPath, err := Install("zsh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(zshPath, tmp) || filepath.Base(zshPath) != "_agy-rotator" {
		t.Fatalf("unexpected zsh install path: %q", zshPath)
	}
	if _, err := os.Stat(zshPath); err != nil {
		t.Fatalf("zsh completion file not written: %v", err)
	}

	bashPath, err := Install("bash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bashPath, tmp) || filepath.Base(bashPath) != "agy-rotator" {
		t.Fatalf("unexpected bash install path: %q", bashPath)
	}
	if _, err := os.Stat(bashPath); err != nil {
		t.Fatalf("bash completion file not written: %v", err)
	}

	if _, err := Install("fish"); err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}
