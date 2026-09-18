package decorator

import (
	"context"
	"io"
	"sync"
)

// StreamingOutput adapts a streaming writer whose Close only releases resources;
// it must not publish staged data. Both completion paths retain already written bytes.
func StreamingOutput(writer io.WriteCloser) Output {
	if output, ok := writer.(Output); ok {
		return output
	}
	return &streamOutput{writer: writer}
}

type streamOutput struct {
	writer io.WriteCloser
	once   sync.Once
	err    error
}

func (s *streamOutput) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *streamOutput) Finish(context.Context) error {
	s.once.Do(func() { s.err = s.writer.Close() })
	return s.err
}

func (s *streamOutput) Abort(context.Context) error {
	var err error
	s.once.Do(func() { s.err = s.writer.Close(); err = s.err })
	return err
}
