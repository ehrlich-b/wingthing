package wing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const maxBrowserAttachmentReceipts = 4096

// Receipts outlive detach, idle expiry and wing restart. They contain only a
// hashed owner/tab/attachment selector and the last consumed sequence, never
// terminal bytes, provider text, credentials or input lease tokens. Capacity
// fails closed rather than evicting a consumed takeover and accepting replay.
type browserAttachmentLedger struct {
	root *os.Root
	once sync.Once
}

func openBrowserAttachmentLedger(dir string) (*browserAttachmentLedger, error) {
	path := filepath.Join(dir, "browser-attachment-receipts-v1")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return nil, errors.New("browser attachment receipts require a private directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &browserAttachmentLedger{root: root}, nil
}
func (l *browserAttachmentLedger) Close() {
	if l != nil {
		l.once.Do(func() { _ = l.root.Close() })
	}
}
func receiptFile(root *os.Root, name string, flags int) (*os.File, error) {
	f, err := root.OpenFile(name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0777 != 0600 {
		_ = f.Close()
		return nil, errors.New("browser receipt is not a private regular file")
	}
	return f, nil
}
func (l *browserAttachmentLedger) Consume(ctx context.Context, key, id string, sequence uint64, attach bool) error {
	if sequence == 0 || sequence > 1<<63-1 || attach && sequence != 1 {
		return errors.New("invalid browser attachment sequence")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lock, err := receiptFile(l.root, ".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(key+"\x00"+id)))
	flags := os.O_RDWR
	if attach {
		directory, err := l.root.Open(".")
		if err != nil {
			return err
		}
		entries, err := directory.ReadDir(maxBrowserAttachmentReceipts + 2)
		_ = directory.Close()
		if err != nil && err != io.EOF {
			return err
		}
		count := 0
		for _, entry := range entries {
			if entry.Name() != ".lock" {
				count++
			}
		}
		if count >= maxBrowserAttachmentReceipts {
			return errors.New("browser takeover receipt capacity reached; no consumed receipt was evicted")
		}
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := receiptFile(l.root, name, flags)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("browser takeover already consumed; replay refused")
		}
		return err
	}
	defer f.Close()
	if !attach {
		data, err := io.ReadAll(io.LimitReader(f, 32))
		if err != nil {
			return err
		}
		last, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 63)
		if err != nil || last == 0 {
			return errors.New("invalid browser attachment receipt; replay fence unavailable")
		}
		if sequence <= last {
			return errors.New("browser attachment sequence already consumed; replay refused")
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", sequence); err != nil {
		return err
	}
	// The receipt must be durable before any takeover or terminal input effect.
	if err := f.Sync(); err != nil {
		return err
	}
	if attach {
		directory, err := l.root.Open(".")
		if err != nil {
			return err
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
