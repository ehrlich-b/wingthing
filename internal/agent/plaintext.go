package agent

import (
	"context"
	"io"
)

// readProviderPlaintext forwards bytes as they arrive, without interpreting line
// endings or UTF-8 boundaries. A fixed buffer bounds each chunk even when the
// provider emits an arbitrarily long line.
func readProviderPlaintext(ctx context.Context, r io.Reader, handle func(string)) error {
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.Read(buf)
		if n > 0 {
			handle(string(buf[:n]))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
