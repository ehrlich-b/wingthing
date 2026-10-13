//go:build !linux

package sandbox

import "fmt"

func CheckProviderWriteProtection() error {
	return fmt.Errorf("Landlock provider write protection requires Linux")
}
