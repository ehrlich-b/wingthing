package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

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
