package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

func processGroupSignalGone(_ *os.Process, err error) bool { return processSignalGone(err) }

func processExited(process *os.Process) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	var code uint32
	err = windows.GetExitCodeProcess(handle, &code)
	return code != 259, err // STILL_ACTIVE
}

func waitProcessExit(process *os.Process) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	_, err = windows.WaitForSingleObject(handle, windows.INFINITE)
	return err
}
