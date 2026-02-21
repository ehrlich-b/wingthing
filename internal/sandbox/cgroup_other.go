//go:build !linux

package sandbox

// cgroupManager is a no-op on non-Linux platforms.
type cgroupManager struct{}

func newCgroupManager(sessionID string, memLimit uint64, pidLimit uint32) (*cgroupManager, error) {
	return nil, nil
}

func (c *cgroupManager) AddPID(pid int) error { return nil }
func (c *cgroupManager) Destroy() error        { return nil }
