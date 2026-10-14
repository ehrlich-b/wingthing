// Package sessionsearch maintains a disposable, bounded index of wing-owned text.
package sessionsearch

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	ReadBudget   = 32 << 20
	SourceBudget = 8 << 20
	TextLimit    = 256 << 10
	RecordLimit  = 1 << 20
)

type Index struct{ db *sql.DB }

// Open uses a private cache separate from the durable task database. A cache
// failure cannot invalidate the sessions; callers report incomplete coverage.
func Open(dir string) (*Index, error) {
	path := filepath.Join(dir, "session-search-v2")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("search cache is not a private directory")
	}
	if err := os.Chmod(path, 0700); err != nil {
		return nil, err
	}
	file := filepath.Join(path, "index.sqlite")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("search cache is not a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA page_size=4096", "PRAGMA busy_timeout=250", "PRAGMA journal_mode=DELETE", "PRAGMA max_page_count=16384", "PRAGMA secure_delete=ON",
		`CREATE TABLE IF NOT EXISTS sources (id INTEGER PRIMARY KEY, session TEXT NOT NULL, name TEXT NOT NULL, device INTEGER, inode INTEGER, size INTEGER, modified INTEGER, offset INTEGER, incomplete INTEGER, prefix TEXT NOT NULL DEFAULT '', checkpoint TEXT NOT NULL DEFAULT '', UNIQUE(session,name))`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS text_index USING fts5(text, tokenize='trigram')`,
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize search cache: %w", err)
		}
	}
	// A cache left by an earlier implementation gets empty fingerprints and
	// is rebuilt before any old text can participate in a changed-source match.
	columns, err := db.Query(`PRAGMA table_info(sources)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	have := map[string]bool{}
	for columns.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			break
		}
		have[name] = true
	}
	if err == nil {
		err = columns.Err()
	}
	_ = columns.Close()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, name := range []string{"prefix", "checkpoint"} {
		if !have[name] {
			if _, err = db.Exec(`ALTER TABLE sources ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
	}
	return &Index{db: db}, nil
}
func (i *Index) Close() error { return i.db.Close() }

// Source journals are append-only. These bounded samples catch replacement by
// truncate-and-regrow without rereading all earlier conversation on every append.
func sourceFingerprints(ctx context.Context, f *os.File, offset int64, budget *int64) (string, string, int64, error) {
	read := int64(0)
	sample := func(start, length int64) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if length > *budget {
			return "", errors.New("search fingerprint budget reached")
		}
		data := make([]byte, int(length))
		n, err := f.ReadAt(data, start)
		*budget -= int64(n)
		read += int64(n)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%x", sha256.Sum256(data)), nil
	}
	prefix, err := sample(0, min(offset, int64(1024)))
	if err != nil {
		return "", "", read, err
	}
	length := min(offset, int64(1024))
	checkpoint, err := sample(offset-length, length)
	return prefix, checkpoint, read, err
}

// Refresh reads only appended complete JSONL records. Immutable gzip archives
// are rebuilt on change. Bounds and malformed records remain visible to callers.
func (i *Index) Refresh(ctx context.Context, session, dir, name string, budget *int64, decode func([]byte) (string, bool)) (bool, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true, i.remove(ctx, session, name)
	}
	if err != nil {
		return false, err
	}
	if !before.Mode().IsRegular() {
		return false, errors.New("transcript is not a regular file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return false, errors.New("transcript identity changed during open")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return false, err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, device, inode, size, modified, offset int64
	var incomplete bool
	var text, prefix, checkpoint string
	err = tx.QueryRowContext(ctx, `SELECT s.id,device,inode,size,modified,offset,incomplete,prefix,checkpoint FROM sources s WHERE session=? AND name=?`, session, name).Scan(&id, &device, &inode, &size, &modified, &offset, &incomplete, &prefix, &checkpoint)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return false, err
	}
	changed := missing || device != int64(stat.Dev) || inode != int64(stat.Ino) || size != info.Size() || modified != info.ModTime().UnixNano()
	compressed := strings.HasSuffix(name, ".gz")
	legacy := !missing && !compressed && (prefix == "" || checkpoint == "")
	if !changed && !legacy && (compressed || offset == info.Size()) {
		return !incomplete, nil
	}
	discard := func() (bool, error) {
		if !missing {
			if _, err := tx.ExecContext(ctx, `DELETE FROM text_index WHERE rowid=?`, id); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE id=?`, id); err != nil {
				return false, err
			}
		}
		return false, tx.Commit()
	}
	// A changed source cannot retain searchable old content when there is no
	// budget to establish that it is still a valid prefix of the current file.
	if *budget <= 0 {
		if changed || legacy {
			return discard()
		}
		return false, nil
	}
	reset := missing || legacy || device != int64(stat.Dev) || inode != int64(stat.Ino) || info.Size() < size || (info.Size() == size && modified != info.ModTime().UnixNano()) || compressed
	fingerprintBytes := int64(0)
	if !reset && changed {
		currentPrefix, currentCheckpoint, read, e := sourceFingerprints(ctx, f, offset, budget)
		fingerprintBytes += read
		if e != nil {
			return discard()
		}
		reset = currentPrefix != prefix || currentCheckpoint != checkpoint
	}
	if reset {
		offset, text, incomplete = 0, "", false
	} else if !missing {
		if err := tx.QueryRowContext(ctx, `SELECT text FROM text_index WHERE rowid=?`, id).Scan(&text); err != nil {
			return false, err
		}
	}
	if *budget <= 0 {
		if changed || reset {
			return discard()
		}
		return false, nil
	}
	var reader io.Reader = f
	if compressed {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return false, err
		}
		defer gz.Close()
		reader = gz
	} else if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}
	// Reserve at most two small samples for the final checkpoint.
	limit := min(max(*budget-2048, 0), int64(SourceBudget)-fingerprintBytes-2048)
	if limit <= 0 {
		return discard()
	}
	limited := &io.LimitedReader{R: reader, N: limit}
	r := bufio.NewReaderSize(limited, 4096)
	partial := false
	var b strings.Builder
	b.WriteString(text)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		line, readErr := r.ReadSlice('\n')
		// Assemble ordinary records while bounding a single record. Oversized
		// records are skipped to the newline, never fed into JSON or regex work.
		bytes := int64(len(line))
		oversized := false
		if readErr == bufio.ErrBufferFull {
			line = append([]byte(nil), line...)
		}
		for readErr == bufio.ErrBufferFull {
			next, e := r.ReadSlice('\n')
			bytes += int64(len(next))
			if len(line)+len(next) <= RecordLimit && !oversized {
				line = append(line, next...)
			} else {
				oversized = true
			}
			readErr = e
		}
		if bytes == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil {
			partial = true
			if readErr != io.EOF {
				incomplete = true
			}
			break
		}
		offset += bytes
		if oversized {
			incomplete = true
			continue
		}
		value, valid := decode(line)
		if !valid {
			incomplete = true
			continue
		}
		value = strings.ToLower(Redact(value))
		if value == "" {
			continue
		}
		if b.Len()+len(value)+1 > TextLimit {
			incomplete = true
			continue
		}
		b.WriteString(value)
		b.WriteByte('\n')
	}
	*budget -= limit - limited.N
	if limited.N == 0 {
		partial = true
	}
	if compressed {
		offset = info.Size()
	} else {
		var e error
		prefix, checkpoint, _, e = sourceFingerprints(ctx, f, offset, budget)
		if e != nil {
			return discard()
		}
	}
	after, e := f.Stat()
	if e != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return discard()
	}
	if missing {
		result, err := tx.ExecContext(ctx, `INSERT INTO sources(session,name) VALUES(?,?)`, session, name)
		if err != nil {
			return false, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return false, err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM text_index WHERE rowid=?`, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO text_index(rowid,text) VALUES(?,?)`, id, b.String()); err != nil {
		return false, err
	}
	// A gzip source cannot resume decompression at an arbitrary byte offset.
	if compressed && partial {
		incomplete = true
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET device=?,inode=?,size=?,modified=?,offset=?,incomplete=?,prefix=?,checkpoint=? WHERE id=?`, int64(stat.Dev), int64(stat.Ino), info.Size(), info.ModTime().UnixNano(), offset, incomplete, prefix, checkpoint, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return !incomplete && !partial, nil
}

func (i *Index) remove(ctx context.Context, session, name string) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM text_index WHERE rowid IN (SELECT id FROM sources WHERE session=? AND name=?)`, session, name); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sources WHERE session=? AND name=?`, session, name); err != nil {
		return err
	}
	return tx.Commit()
}

// Invalidate excludes a source that can no longer be read safely. No stale
// positive match from a replaced symlink or inaccessible file survives refresh.
func (i *Index) Invalidate(ctx context.Context, session, name string) error {
	return i.remove(ctx, session, name)
}

// Prune discards sessions deleted from a fully enumerated directory. Do not
// call this with an authority-filtered or truncated inventory.
func (i *Index) Prune(ctx context.Context, sessions []string) error {
	if len(sessions) > 1000 {
		return errors.New("search prune bound exceeded")
	}
	predicate := "1=1"
	args := []any{}
	if len(sessions) > 0 {
		predicate = `session NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(sessions)), ",") + `)`
		for _, session := range sessions {
			args = append(args, session)
		}
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM text_index WHERE rowid IN (SELECT id FROM sources WHERE `+predicate+`)`, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE `+predicate, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// Matches combines indexed substring lookup with a bounded fallback for queries
// shorter than one trigram. Words may match different records or metadata fields.
func (i *Index) Matches(ctx context.Context, session string, words []string) ([]bool, error) {
	matches, err := i.MatchesMany(ctx, []string{session}, words)
	if err != nil {
		return nil, err
	}
	if result := matches[session]; result != nil {
		return result, nil
	}
	return make([]bool, len(words)), nil
}

// MatchesMany evaluates each query word once across the admitted session IDs.
// The caller has already checked every session's current ownership and paths.
func (i *Index) MatchesMany(ctx context.Context, sessions, words []string) (map[string][]bool, error) {
	out := map[string][]bool{}
	if len(sessions) == 0 {
		return out, nil
	}
	if len(sessions) > 1000 || len(words) > 8 {
		return nil, errors.New("search candidate bound exceeded")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sessions)), ",")
	for n, word := range words {
		statement := `SELECT DISTINCT s.session FROM text_index JOIN sources s ON s.id=text_index.rowid WHERE s.session IN (` + placeholders + `) AND instr(text,?)>0`
		args := make([]any, 0, len(sessions)+2)
		for _, session := range sessions {
			args = append(args, session)
		}
		if utf8.RuneCountInString(word) >= 3 {
			statement += ` AND text_index MATCH ?`
			args = append(args, word, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
		} else {
			args = append(args, word)
		}
		rows, err := i.db.QueryContext(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var session string
			if err := rows.Scan(&session); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if out[session] == nil {
				out[session] = make([]bool, len(words))
			}
			out[session][n] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Excerpt reads at most 240 characters from an already admitted session's
// redacted cache. It never queries another session or reads a transcript file.
func (i *Index) Excerpt(ctx context.Context, session string, words []string) (string, error) {
	if session == "" || len(words) > 8 {
		return "", errors.New("invalid excerpt query")
	}
	for _, word := range words {
		if word == "" {
			continue
		}
		var text string
		err := i.db.QueryRowContext(ctx, `SELECT substr(text,max(1,instr(text,?)-80),240) FROM text_index JOIN sources s ON s.id=text_index.rowid WHERE s.session=? AND instr(text,?)>0 LIMIT 1`, word, session, word).Scan(&text)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		return strings.Join(strings.Fields(text), " "), nil
	}
	return "", nil
}
