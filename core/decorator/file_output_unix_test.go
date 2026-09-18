//go:build !windows

package decorator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func outputFIFO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalFileOutputCancelWaitingForFIFOReader(t *testing.T) {
	path := outputFIFO(t)
	session := NewLocalSession()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			output, err := session.OpenFileOutput(ctx, path, FileTruncate, 0o600)
			if output != nil {
				_ = output.Abort(context.Background())
			}
			done <- err
		}()
		// Wait until opening is suspended for lack of a reader, not for a
		// guessed interval. Cancellation must end the open without a peer.
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("open: got %v, want context cancellation", err)
		}
	})
}

func TestLocalFileOutputFIFOReaderCanArriveLater(t *testing.T) {
	path := outputFIFO(t)
	session := NewLocalSession()
	synctest.Test(t, func(t *testing.T) {
		opened := make(chan Output, 1)
		failed := make(chan error, 1)
		go func() {
			output, err := session.OpenFileOutput(context.Background(), path, FileTruncate, 0o600)
			if err != nil {
				failed <- err
				return
			}
			opened <- output
		}()
		synctest.Wait()
		reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		var output Output
		select {
		case output = <-opened:
		case err := <-failed:
			t.Fatal(err)
		}
		defer output.Abort(context.Background())
		if _, err := output.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		if err := output.Finish(context.Background()); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "payload" {
			t.Fatalf("read: got %q, %v", data, err)
		}
	})
}

func TestLocalFileOutputCancelBlockedFIFOWrite(t *testing.T) {
	path := outputFIFO(t)
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output, err := NewLocalSession().OpenFileOutput(ctx, path, FileTruncate, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Abort(context.Background())

	// The reader consumes only one byte from a write far larger than the
	// FIFO buffer. Seeing that byte proves the write has started; no sleep
	// is needed to arrange backpressure before cancellation.
	payload := bytes.Repeat([]byte("x"), 16<<20)
	done := make(chan error, 1)
	go func() {
		_, err := output.Write(payload)
		done <- err
	}()
	if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write: got %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		// Release a broken implementation before test cleanup.
		_ = reader.Close()
		<-done
		t.Fatal("cancellation did not unblock the FIFO write")
	}
}
