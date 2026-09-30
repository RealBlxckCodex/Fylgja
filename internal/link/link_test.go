package link

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	os.Symlink("/etc", filepath.Join(dir, "etc"))
	if !within(filepath.Join(dir, "a.txt"), []string{dir}) || within("/etc/passwd", []string{dir}) || within(filepath.Join(dir, "etc", "passwd"), []string{dir}) {
		t.Fatal("scope-prüfung")
	}
	allow := []string{"git status", "ls"}
	for cmd, want := range map[string]bool{"git status": true, "git status -s": true, "ls -la": true, "git push": false, "ls; rm -rf ~": false, "ls $(whoami)": false, "lsblk": false} {
		if AllowedCommand(cmd, allow) != want {
			t.Errorf("%q", cmd)
		}
	}
}
