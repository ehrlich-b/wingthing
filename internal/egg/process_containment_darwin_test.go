//go:build darwin

package egg

import "errors"

func newTestCgroup() (processBoundary, error) { return nil, errors.New("cgroups require Linux") }
