package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONKeepsBackupAndMode(t *testing.T) {
	dir := t.TempDir()
	file, bak := filepath.Join(dir, "d.json"), filepath.Join(dir, "d.bak.json")
	opt := WriteOptions{TmpFile: filepath.Join(dir, "d.tmp"), BackupFile: bak}
	if err := WriteJSON(file, map[string]int{"v": 1}, opt); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(file, map[string]int{"v": 2}, opt); err != nil {
		t.Fatal(err)
	}
	var cur, prev map[string]int
	if ReadJSON(file, &cur) != nil || ReadJSON(bak, &prev) != nil || cur["v"] != 2 || prev["v"] != 1 {
		t.Fatalf("cur=%v prev=%v", cur, prev)
	}
	for _, f := range []string{file, bak} {
		if st, _ := os.Stat(f); st.Mode().Perm() != FileMode {
			t.Fatalf("%s mode %o: data files must be owner-only", filepath.Base(f), st.Mode().Perm())
		}
	}
	if _, err := os.Stat(opt.TmpFile); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}
