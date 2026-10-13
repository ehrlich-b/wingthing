package egg

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

var errLifecycleReadBudget = errors.New("lifecycle search read budget reached")

// TryReadSessionLifecycleBounded preserves native identity and journal locking,
// while sharing the catalog's finite budget and cancellation across imports.
func TryReadSessionLifecycleBounded(ctx context.Context, dir, agent, cwd, home, providerID string, alive bool, after int64, limit int, remaining *int64) (SessionView, error) {
	if remaining == nil || *remaining <= 0 {
		return SessionView{}, errLifecycleReadBudget
	}
	if err := ctx.Err(); err != nil {
		return SessionView{}, err
	}
	budget := &lifecycleReadBudget{ctx: ctx, remaining: remaining, sourceRemaining: 8 << 20}
	return readSessionLifecycle(dir, agent, cwd, home, providerID, alive, after, limit, unix.LOCK_EX|unix.LOCK_NB, budget)
}

type boundedLifecycleReader struct {
	reader io.Reader
	budget *lifecycleReadBudget
}

func (r boundedLifecycleReader) Read(p []byte) (int, error) {
	if err := r.budget.ctx.Err(); err != nil {
		return 0, err
	}
	left := min(*r.budget.remaining, r.budget.sourceRemaining)
	if left <= 0 {
		return 0, errLifecycleReadBudget
	}
	if int64(len(p)) > left {
		p = p[:left]
	}
	n, err := r.reader.Read(p)
	*r.budget.remaining -= int64(n)
	r.budget.sourceRemaining -= int64(n)
	return n, err
}
func (j *lifecycleJournal) boundedReader(reader io.Reader) io.Reader {
	if j.readBudget == nil {
		return reader
	}
	return boundedLifecycleReader{reader: reader, budget: j.readBudget}
}
