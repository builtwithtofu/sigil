package executor

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/sdk"
)

// runOutput owns the sink lifetime, even when producers ignore writer errors.
func (e *executor) runOutput(execCtx sdk.ExecutionContext, identity, transport string,
	open func(context.Context) (decorator.Output, error), run func(sdk.ExecutionContext, io.Writer) int) int {
	ctx, cancel := context.WithCancel(execCtx.Context())
	defer cancel()
	writer, err := open(ctx)
	if err == nil && writer == nil {
		err = errors.New("provider returned no output")
	}
	if err != nil {
		e.reportSinkError(identity, "open", transport, err)
		return decorator.ExitFailure
	}
	capture := &sinkWriteErrorCapture{writer: writer, cancel: cancel}
	code := run(execCtx.WithContext(ctx), capture)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	writeErr := capture.Err()
	var finishErr error
	phase := "finish"
	if code == 0 && writeErr == nil && ctx.Err() == nil {
		finishErr = writer.Finish(ctx)
		if finishErr != nil {
			finishErr = errors.Join(finishErr, writer.Abort(cleanupCtx))
		}
	} else {
		phase = "abort"
		finishErr = writer.Abort(cleanupCtx)
	}
	if code != 0 && (writeErr != nil || finishErr != nil) {
		e.recordOutputError(ProducerError{ExitCode: code, Cause: execCtx.Context().Err()})
	}
	if writeErr != nil {
		e.reportSinkError(identity, "write", transport, writeErr)
	}
	if finishErr != nil {
		e.reportSinkError(identity, phase, transport, finishErr)
	}
	if execCtx.Context().Err() != nil {
		return decorator.ExitCanceled
	}
	if writeErr != nil || finishErr != nil {
		return decorator.ExitFailure
	}
	return code
}
