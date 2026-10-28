// Package fsx holds crash-safe file helpers.
package fsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Data files hold stream links (signed tokens), sessions and client addresses: owner-only.
const (
	FileMode os.FileMode = 0o600
	DirMode  os.FileMode = 0o700
)

type WriteOptions struct {
	TmpFile    string      // default: <file>.tmp
	BackupFile string      // when set, the previous file is copied here first
	Mode       os.FileMode // default FileMode
	Pretty     bool
}

// WriteJSON writes v atomically: temp file → fsync → rename over the target → fsync the directory.
// A power cut leaves either the old file or the new one, never a half-written file.
func WriteJSON(file string, v any, opt WriteOptions) error {
	var data []byte
	var err error
	if opt.Pretty {
		data, err = json.MarshalIndent(v, "", "  ")
	} else {
		data, err = json.Marshal(v)
	}
	if err != nil {
		return fmt.Errorf("fsx: encode %s: %w", filepath.Base(file), err)
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	if opt.BackupFile != "" {
		if err := copyFile(file, opt.BackupFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("fsx: backup %s: %w", filepath.Base(file), err)
		}
	}
	tmp := opt.TmpFile
	if tmp == "" {
		tmp = file + ".tmp"
	}
	mode := opt.Mode
	if mode == 0 {
		mode = FileMode
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil { // a leftover temp file keeps its old mode otherwise
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil { // not every platform can fsync a directory
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ReadJSON decodes file into v.
func ReadJSON(file string, v any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("fsx: invalid %s: %w", filepath.Base(file), err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, FileMode)
	if err != nil {
		return err
	}
	if err := out.Chmod(FileMode); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
