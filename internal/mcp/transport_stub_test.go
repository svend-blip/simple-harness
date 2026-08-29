package mcp

import (
	"context"
	"sync"
)

// stubTransport is a test-only Transport that returns canned listings
// and records calls. NOT exported; lives in *_test.go (this file is
// the seam the unit tests use until WORK 2 ships the production http
// + stdio transports; production code paths must NOT import this
// stub).
//
// The stub's behavior is controlled by:
//   - listings: the canned listing List() returns.
//   - nextListErr / nextCallErr: optional errors List / Call return
//     when set. The error is returned once and cleared, so a single
//     test can simulate "list fails then list succeeds" without
//     re-instantiating the stub.
//   - calls: append-only record of every Call invocation (name +
//     args). Tests assert on this slice.
//
// Concurrency: the stub takes a mutex around the mutable state
// (nextListErr / nextCallErr / calls). Production transports do not
// need this; the tests use it sparingly.
type stubTransport struct {
	mu          sync.Mutex
	listings    []ListedTool
	calls       []stubCall
	nextListErr error
	nextCallErr error
}

// stubCall records one transport.Call invocation. The Name field is
// what the adapter passes (the original name, not the collision-
// resolved FinalName); the Args field is the verbatim call.Arguments.
type stubCall struct {
	Name string
	Args map[string]interface{}
}

// List returns the stub's canned listings, or nextListErr if set. The
// error is cleared after being returned (one-shot semantics) so a
// test that wants "list fails then list succeeds" can set the error,
// call List, then call List again on the same stub.
func (s *stubTransport) List(_ context.Context) ([]ListedTool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextListErr != nil {
		err := s.nextListErr
		s.nextListErr = nil
		return nil, err
	}
	// Return a defensive copy so the test's later mutation of the
	// original slice does not affect the stub's view.
	out := make([]ListedTool, len(s.listings))
	copy(out, s.listings)
	return out, nil
}

// Call records the invocation and returns a synthetic map[string]
// interface{} result. The result shape is deterministic: the stub
// echoes {"name": <call.Name>, "echoed": true}. A test that needs a
// specific result should construct a custom transport; the unit tests
// use the echo shape and inspect the recorded calls for assertion.
func (s *stubTransport) Call(_ context.Context, name string, args map[string]interface{}) (map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubCall{Name: name, Args: args})
	if s.nextCallErr != nil {
		err := s.nextCallErr
		s.nextCallErr = nil
		return nil, err
	}
	return map[string]interface{}{
		"echoed": true,
		"name":   name,
		"args":   args,
	}, nil
}

// Close is a no-op for the stub. The test cleanup path that wants to
// verify Close was called should wrap the stub in a counting wrapper.
func (s *stubTransport) Close() error { return nil }

// Compile-time assertion that stubTransport satisfies Transport.
var _ Transport = (*stubTransport)(nil)

// newStubTransport constructs a stubTransport with the given listings.
// Tests that want to inject errors set nextListErr / nextCallErr
// directly on the returned stub.
func newStubTransport(listings []ListedTool) *stubTransport {
	return &stubTransport{listings: listings}
}