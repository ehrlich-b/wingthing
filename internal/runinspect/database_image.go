package runinspect

import (
	"bytes"
	"io/fs"
	"time"
)

// databaseImage exposes only the exact hash-verified wt.db bytes to SQLite's
// read-only fs.FS VFS. No pathname is reopened and no temporary file is created.
type databaseImage []byte

func (image databaseImage) Open(name string) (fs.File, error) {
	if name != "wt.db" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &databaseImageFile{bytes.NewReader(image)}, nil
}

type databaseImageFile struct{ *bytes.Reader }

func (f *databaseImageFile) Close() error { return nil }
func (f *databaseImageFile) Stat() (fs.FileInfo, error) {
	return databaseImageInfo(f.Size()), nil
}

type databaseImageInfo int64

func (databaseImageInfo) Name() string       { return "wt.db" }
func (info databaseImageInfo) Size() int64   { return int64(info) }
func (databaseImageInfo) Mode() fs.FileMode  { return 0400 }
func (databaseImageInfo) ModTime() time.Time { return time.Time{} }
func (databaseImageInfo) IsDir() bool        { return false }
func (databaseImageInfo) Sys() any           { return nil }
