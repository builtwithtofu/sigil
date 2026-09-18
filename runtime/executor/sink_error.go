package executor

import (
	"errors"
	"fmt"

	"github.com/builtwithtofu/sigil/core/decorator"
)

type SinkError struct {
	SinkID      string
	Operation   string
	TransportID string
	Cause       error
}

func (e SinkError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("sink %s %s failed on transport %s", e.SinkID, e.Operation, e.TransportID)
	}
	return fmt.Sprintf("sink %s %s failed on transport %s: %v", e.SinkID, e.Operation, e.TransportID, e.Cause)
}

func (e SinkError) Unwrap() error {
	return e.Cause
}

// ProducerError preserves the producer outcome when sink cleanup also fails.
type ProducerError struct {
	ExitCode int
	Cause    error
}

func (e ProducerError) Error() string {
	return fmt.Sprintf("producer exited with status %d", e.ExitCode)
}
func (e ProducerError) Unwrap() error { return e.Cause }

func (e *executor) reportSinkError(identity, phase, transport string, cause error) {
	err := SinkError{SinkID: identity, Operation: phase, TransportID: transport, Cause: cause}
	e.recordOutputError(err)
	if errors.Is(cause, decorator.ErrPublicationUnknown) && e.stop != nil {
		e.stop(err)
	}
}

func (e *executor) recordOutputError(err error) {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	e.sinkErrors = append(e.sinkErrors, err)
	_, _ = fmt.Fprintf(e.getStderr(), "Error: %v\n", err)
}
