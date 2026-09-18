package decorator

import "io"

// IOCaps declares endpoint operations. Publication guarantees belong to the
// requested output policy, not a global atomicity flag on a provider.
type IOCaps struct {
	Read   bool
	Write  bool
	Append bool
}

// IO identifies a registered endpoint. Input and output are independent capabilities.
type IO interface {
	Decorator
	IOCaps() IOCaps
}

type Source interface {
	IO
	OpenRead(ExecContext) (io.ReadCloser, error)
}

type Sink interface {
	IO
	OpenWrite(ExecContext, bool) (Output, error)
}

// IOFactory binds already validated endpoint arguments to an invocation.
// The bound endpoint must not mutate the registered provider.
type IOFactory interface {
	IO
	WithParams(map[string]any) IO
}
