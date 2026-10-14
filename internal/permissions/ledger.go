package permissions

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Actor struct {
	Principal string `json:"principal"`
	Client    string `json:"client"`
	Surface   string `json:"surface"`
}
type record struct {
	Response
	Actor
	Time          time.Time `json:"time"`
	Authorization string    `json:"authorization"`
	Phase         string    `json:"phase,omitempty"`
	Outcome       Outcome   `json:"outcome"`
}

// Ledger uses a private, fsynced reservation and a cross-process exclusive lock.
// An unconfirmed reservation is final for delivery purposes, including restart.
type Ledger struct{ Dir string }

func (l Ledger) locked(fn func() (Outcome, error)) (Outcome, error) {
	if err := os.MkdirAll(l.Dir, 0700); err != nil {
		return Outcome{}, err
	}
	directory, err := os.OpenFile(l.Dir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Outcome{}, err
	}
	defer directory.Close()
	parent, err := os.Open(filepath.Dir(l.Dir))
	if err != nil {
		return Outcome{}, err
	}
	err = parent.Sync()
	err = errors.Join(err, parent.Close())
	if err != nil {
		return Outcome{}, err
	}
	if err = directory.Chmod(0700); err != nil {
		return Outcome{}, err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, "lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return Outcome{}, err
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return Outcome{}, err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}
func (l Ledger) audit(v any) error {
	f, err := os.OpenFile(filepath.Join(l.Dir, "audit.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(v)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	directory, err := os.Open(l.Dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (l Ledger) Refuse(r Response, a Actor, reason string) error {
	_, err := l.locked(func() (Outcome, error) {
		return Outcome{}, l.audit(record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: reason})
	})
	return err
}
func (l Ledger) Decide(r Response, a Actor, authorize func() error, live func() error, deliver func() (Outcome, error)) (Outcome, error) {
	return l.locked(func() (Outcome, error) {
		if r.WingID == "" || r.SessionID == "" || r.RequestID == "" || r.Digest == "" || r.AttemptID == "" || len(r.AttemptID) > 128 || (r.Decision != "allow" && r.Decision != "deny") {
			return Outcome{}, errors.New("complete decision binding required")
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.WingID+"\x00"+r.SessionID+"\x00"+r.RequestID)))
		path := filepath.Join(l.Dir, key+".json")
		file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(file, 8193))
			err = errors.Join(err, file.Close())
			if len(data) > 8192 {
				return Outcome{}, errors.New("invalid decision record size")
			}
		}
		if err == nil {
			var old record
			if json.Unmarshal(data, &old) != nil {
				return Outcome{}, errors.New("invalid decision ledger")
			}
			if old.Response == r && old.Actor == a {
				return old.Outcome, nil
			}
			if err = l.audit(record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: "conflict"}); err != nil {
				return Outcome{}, err
			}
			return Outcome{}, errors.New("decision conflicts with a reserved answer")
		}
		if !os.IsNotExist(err) {
			return Outcome{}, err
		}
		attemptKey := fmt.Sprintf("%x", sha256.Sum256([]byte(a.Principal+"\x00"+a.Client+"\x00"+a.Surface+"\x00"+r.AttemptID)))
		attemptPath := filepath.Join(l.Dir, "attempt-"+attemptKey+".json")
		if attemptData, readErr := os.ReadFile(attemptPath); readErr == nil {
			var old record
			if json.Unmarshal(attemptData, &old) != nil {
				return Outcome{}, errors.New("invalid attempt ledger")
			}
			if old.Response == r && old.Actor == a {
				return old.Outcome, nil
			}
			if err = l.audit(record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: "attempt_conflict"}); err != nil {
				return Outcome{}, err
			}
			return Outcome{}, errors.New("decision attempt is bound to a different request")
		} else if !os.IsNotExist(readErr) {
			return Outcome{}, readErr
		}
		if err = authorize(); err != nil {
			auditErr := l.audit(record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: "denied"})
			return Outcome{}, errors.Join(err, auditErr)
		}
		if err = live(); err != nil {
			auditErr := l.audit(record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: "refused", Outcome: Outcome{State: "failed", Decision: r.Decision, AttemptID: r.AttemptID}})
			return Outcome{}, errors.Join(err, auditErr)
		}
		entry := record{Response: r, Actor: a, Time: time.Now().UTC(), Authorization: "allowed", Phase: "reservation", Outcome: Outcome{State: "unconfirmed", Decision: r.Decision, AttemptID: r.AttemptID}}
		// Audit before reserving: a failed audit has never reached delivery and
		// must leave neither a request nor an attempt marked unconfirmed.
		if err = l.audit(entry); err != nil {
			return Outcome{}, err
		}
		if err = l.persist(attemptPath, entry); err != nil {
			return Outcome{}, err
		}
		if err = l.persist(path, entry); err != nil {
			return Outcome{}, err
		}
		// Authority can be revoked while persistence is in progress.
		if err = authorize(); err != nil {
			entry.Time, entry.Phase, entry.Authorization = time.Now().UTC(), "delivery", "denied_before_delivery"
			entry.Outcome.State = "failed"
			auditErr := l.audit(entry)
			persistErr := errors.Join(l.persist(path, entry), l.persist(attemptPath, entry))
			return entry.Outcome, errors.Join(err, auditErr, persistErr)
		}
		result, deliveryErr := deliver()
		if result.State != "delivered" && result.State != "failed" {
			result = entry.Outcome
		}
		entry.Time = time.Now().UTC()
		entry.Phase = "delivery"
		entry.Outcome = result
		// Append the delivery receipt separately from the reservation. A transport
		// loss remains unconfirmed; a known refusal is failed, never a lost reply.
		auditErr := l.audit(entry)
		persistErr := l.persist(path, entry)
		if persistErr == nil {
			persistErr = l.persist(attemptPath, entry)
		}
		return result, errors.Join(deliveryErr, auditErr, persistErr)
	})
}
func (l Ledger) persist(path string, v any) error {
	f, err := os.CreateTemp(l.Dir, ".decision-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	err = json.NewEncoder(f).Encode(v)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(l.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
