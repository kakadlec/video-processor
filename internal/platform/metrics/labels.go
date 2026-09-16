package metrics

import "sync/atomic"

// The two fallbacks. A route or a method the bound table does not contain
// collapses to one of these, so the number of series a service can produce is
// a product of numbers the build fixes rather than a quantity that grows with
// traffic.
//
// UnrecognizedMethod reuses the spelling the access record already uses for
// the same fact, so one unrecognized method reads the same in a record and in
// a series. The request path is deliberately not a fallback of any kind: the
// access record keeps a truncated path for an unmatched request, and a metric
// may not copy it, because truncation bounds a value's length and not the
// number of distinct values.
const (
	UnmatchedRoute     = "<unmatched>"
	UnrecognizedMethod = "UNRECOGNIZED"
)

// RouteEntry is one row of a router's route table, in this repository's own
// representation. The HTTP framework's own type is deliberately not used:
// nothing under internal/ imports that framework, this package's dependency
// test says so in terms, and what crosses the boundary here is two strings.
// Each composition root translates its own router's table at the point it
// binds it.
type RouteEntry struct {
	Method string
	Path   string
}

// routeTable is the bound domain of both label constructors. It is replaced
// wholesale rather than mutated, so a reader never observes a half-built one.
type routeTable struct {
	routes  map[string]bool
	methods map[string]bool
}

var boundRoutes atomic.Pointer[routeTable]

// BindRoutes binds the domain of the route and method labels to the table the
// router reports. Each composition root calls it once, as the last statement
// of its router construction: the table is read from the router at the moment
// of the call, so binding before the last route is registered would send a
// real route to the unmatched value for the life of the process.
func BindRoutes(entries []RouteEntry) {
	table := &routeTable{
		routes:  make(map[string]bool, len(entries)),
		methods: make(map[string]bool, len(entries)),
	}
	for _, entry := range entries {
		table.routes[entry.Path] = true
		table.methods[entry.Method] = true
	}
	boundRoutes.Store(table)
}

// Route resolves a matched route template against the bound table. It is
// total: every input returns a member of a bounded set.
//
// The middleware passes the template the router itself matched, which is a
// member of the table by construction, so the lookup is belt-and-braces —
// and that is the point. It makes the bound structural rather than a property
// of where the value came from, so a later call site that passes something
// else cannot widen the domain.
//
// Resolution is fail-closed: with no table bound, every route resolves to the
// unmatched value rather than passing through.
func Route(route string) string {
	table := boundRoutes.Load()
	if table == nil || !table.routes[route] {
		return UnmatchedRoute
	}
	return route
}

// Method resolves a request method against the bound table, on the same terms
// as Route and fail-closed in the same direction.
func Method(method string) string {
	table := boundRoutes.Load()
	if table == nil || !table.methods[method] {
		return UnrecognizedMethod
	}
	return method
}
