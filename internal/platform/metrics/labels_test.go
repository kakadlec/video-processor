package metrics

import "testing"

// These tests replace the process-wide route table, which is shared state
// exactly as the process-wide logger is, so none of them is parallel.

// withTable binds entries for the duration of the test and restores whatever
// was bound before, so an assertion about the unbound table does not depend
// on which test ran first.
func withTable(t *testing.T, entries []RouteEntry) {
	t.Helper()

	previous := boundRoutes.Load()
	t.Cleanup(func() { boundRoutes.Store(previous) })
	if entries == nil {
		boundRoutes.Store(nil)
		return
	}
	BindRoutes(entries)
}

// TestResolutionIsFailClosedUntilTheTableIsBound holds the property a
// behavioural test in a composition root cannot reach, because every root
// binds its table before serving anything. An unbound table that passed
// values through would be an unbounded series count in exactly the process
// where nobody would think to look for one — and it is the state a root that
// forgot the call leaves the package in.
func TestResolutionIsFailClosedUntilTheTableIsBound(t *testing.T) {
	withTable(t, nil)

	for _, route := range []string{"/api/status", "/anything", ""} {
		if got := Route(route); got != UnmatchedRoute {
			t.Errorf("Route(%q) = %q with no table bound, want %q", route, got, UnmatchedRoute)
		}
	}
	for _, method := range []string{"GET", "POST", "PROPFIND", ""} {
		if got := Method(method); got != UnrecognizedMethod {
			t.Errorf("Method(%q) = %q with no table bound, want %q", method, got, UnrecognizedMethod)
		}
	}
}

// TestBothConstructorsAreTotal is the property that makes them admissible
// where a rendered value is not: every input returns a member of a bounded
// set, including an input neither recognizes.
func TestBothConstructorsAreTotal(t *testing.T) {
	withTable(t, []RouteEntry{
		{Method: "GET", Path: "/api/video-jobs/:id"},
		{Method: "POST", Path: "/upload"},
	})

	for _, tc := range []struct{ in, want string }{
		{in: "/api/video-jobs/:id", want: "/api/video-jobs/:id"},
		{in: "/upload", want: "/upload"},
		{in: "/api/video-jobs/9b2f1c44-7d3a-4f2e-8a1b-5c6d7e8f9a0b", want: UnmatchedRoute},
		{in: "/../../etc/passwd", want: UnmatchedRoute},
		{in: "", want: UnmatchedRoute},
	} {
		if got := Route(tc.in); got != tc.want {
			t.Errorf("Route(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, tc := range []struct{ in, want string }{
		{in: "GET", want: "GET"},
		{in: "POST", want: "POST"},
		// Not in this table, though it is a method this repository serves
		// elsewhere: the domain is the router's own table and not the set of
		// methods that exist.
		{in: "PUT", want: UnrecognizedMethod},
		{in: "PROPFIND", want: UnrecognizedMethod},
		{in: "", want: UnrecognizedMethod},
	} {
		if got := Method(tc.in); got != tc.want {
			t.Errorf("Method(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRebindingReplacesTheWholeTable holds the shape the binding is written
// in. The table is replaced rather than merged, so a route that has gone away
// stops resolving to itself — a merge would make the domain grow with every
// binding rather than be fixed by the last one.
func TestRebindingReplacesTheWholeTable(t *testing.T) {
	withTable(t, []RouteEntry{{Method: "GET", Path: "/first"}})
	if Route("/first") != "/first" {
		t.Fatal("the first binding did not take")
	}

	BindRoutes([]RouteEntry{{Method: "POST", Path: "/second"}})

	if got := Route("/first"); got != UnmatchedRoute {
		t.Errorf("Route(%q) = %q after rebinding, want %q", "/first", got, UnmatchedRoute)
	}
	if got := Method("GET"); got != UnrecognizedMethod {
		t.Errorf("Method(%q) = %q after rebinding, want %q", "GET", got, UnrecognizedMethod)
	}
	if got := Route("/second"); got != "/second" {
		t.Errorf("Route(%q) = %q after rebinding, want itself", "/second", got)
	}
}

// TestTheRuntimeCollectorsAreRegistered holds decision 12: they are named
// rather than obtained, because the client library's default registry carries
// both already and using it is the registry this capability refuses. Same
// series, different property — what the endpoint exposes is readable from
// this repository.
func TestTheRuntimeCollectorsAreRegistered(t *testing.T) {
	families, err := Gatherer().Gather()
	if err != nil {
		t.Fatalf("gathering failed: %v", err)
	}

	wanted := map[string]bool{"go_goroutines": false, "go_info": false, "process_open_fds": false}
	for _, family := range families {
		if _, looked := wanted[family.GetName()]; looked {
			wanted[family.GetName()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("%s is absent; the runtime collectors are the only signal this system has for a goroutine leak or descriptor exhaustion", name)
		}
	}
}
