package wing

import (
	"context"
	"crypto/rand"
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
	root  *os.Root
	write func(*os.File, uint64) error
	once  sync.Once
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
	writer := l.write
	if writer == nil {
		writer = writeBrowserReceipt
	}
	if attach {
		// The initial single-use ID was created exclusively. No previous
		// high-water mark can be lowered by an initial publication failure.
		if err := writer(f, sequence); err != nil {
			return err
		}
	} else {
		// Preserve the previous fence if writing or syncing a replacement
		// fails. A short decimal prefix can never replace a consumed sequence.
		temporary := ".pending-" + rand.Text()
		replacement, err := receiptFile(l.root, temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR)
		if err != nil {
			return err
		}
		defer l.root.Remove(temporary)
		err = writer(replacement, sequence)
		closeErr := replacement.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := l.root.Rename(temporary, name); err != nil {
			return err
		}
	}
	// Persist the new name/replacement before any terminal effect. Directory
	// sync failure may consume a sequence, but can never lower its prior fence.
	directory, err := l.root.Open(".")
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}
func writeBrowserReceipt(f *os.File, sequence uint64) error {
	data := []byte(fmt.Sprintf("%d\n", sequence))
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return f.Sync()
}
