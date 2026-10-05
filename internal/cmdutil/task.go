package cmdutil

import (
	"fmt"
	"time"
)

func GenTaskID() string {
	return fmt.Sprintf("t-%s-%s", time.Now().Format("20060102-150405"), NewRuntimeID())
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
