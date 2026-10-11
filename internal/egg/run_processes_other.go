//go:build !darwin

package egg

func sessionGroupSignalError(_ int, signalErr error) error {
	return signalErr
}
