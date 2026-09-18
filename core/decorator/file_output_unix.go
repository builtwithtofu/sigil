//go:build !windows

package decorator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"
	"time"
)

func openFileForOutput(ctx context.Context, path string, flags int, perm fs.FileMode) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Nonblocking descriptors let os.File's poller interrupt FIFO writes
		// when Close is called. A FIFO without a reader fails with ENXIO
		// instead of trapping the caller inside open(2).
		file, err := os.OpenFile(path, flags|syscall.O_NONBLOCK, perm)
		if !errors.Is(err, syscall.ENXIO) {
			return file, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode()&fs.ModeNamedPipe == 0 {
			return nil, err
		}
		// There is no descriptor to poll until a reader arrives. Retry only
		// this FIFO rendezvous, with cancellation interrupting the wait.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
