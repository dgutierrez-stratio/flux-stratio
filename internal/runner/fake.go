package runner

import "context"

// FakeCall records one invocation made through a Fake Runner.
type FakeCall struct {
	Name string
	Args []string
}

// FakeResponse is the canned result a Fake Runner returns for one binary
// name, regardless of the arguments passed.
type FakeResponse struct {
	Stdout, Stderr []byte
	Err            error
}

// Fake is a Runner for tests: it returns a canned FakeResponse keyed by
// binary name and records every call it receives, so a test can assert both
// on the exit path (return values) and on exactly what would have been
// executed, with no real binary installed.
type Fake struct {
	Responses map[string]FakeResponse
	Calls     []FakeCall
}

// Run implements Runner. It errors if no response was configured for name,
// so an unexpected call fails loudly instead of silently returning zero
// values.
func (f *Fake) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.Calls = append(f.Calls, FakeCall{Name: name, Args: args})
	resp, ok := f.Responses[name]
	if !ok {
		return nil, nil, &unconfiguredError{name: name}
	}
	return resp.Stdout, resp.Stderr, resp.Err
}

type unconfiguredError struct{ name string }

func (e *unconfiguredError) Error() string {
	return "fake runner: no response configured for " + e.name
}
