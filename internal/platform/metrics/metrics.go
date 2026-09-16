// Package metrics builds the one registry each process records into and the
// exposition handler the HTTP services serve it through. It holds the
// mechanism — the registry, the handler, and the two total label
// constructors — and no metric family belonging to a bounded context: a
// family is declared in the package that records it, the way a record is
// emitted where the event happens.
//
// It imports no HTTP framework and no bounded context.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry is this process's only registry, and it is constructed rather than
// inherited. The client library's default registry is process-global mutable
// state any transitively imported package can write a family into, with no
// call site here at all; an explicit one is what makes "what this endpoint
// exposes" a question answerable by reading this repository.
var registry = newRegistry()

func newRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	// The runtime's own collectors, named rather than obtained: the default
	// registry carries both already, and getting them by using it would be
	// the same series with a different property. They are the only signal
	// this system has for a goroutine leak, heap growth, or descriptor
	// exhaustion, in a process running relays, a sweeper, consumers and a
	// lease heartbeat as goroutines.
	//
	// The options value is not optional: NewProcessCollector takes one and
	// its zero value selects the defaults, while NewGoCollector is variadic.
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// MustRegister adds a family, or a collector, to this process's registry. It
// panics on a duplicate or inconsistent registration, which is a programming
// error discovered at startup rather than at the first scrape.
func MustRegister(cs ...prometheus.Collector) {
	registry.MustRegister(cs...)
}

// Gatherer exposes the registry as a read-only gatherer, for a test that
// reads what a family recorded. A test that does so manipulates
// process-global state and cannot be parallel — the cost slog.SetDefault
// already carries.
func Gatherer() prometheus.Gatherer {
	return registry
}

// Handler serves the exposition. Error handling is the library's default: a
// collector that reports a failure makes the scrape itself fail, which is the
// behaviour a collected gauge depends on to report a value it does not have.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
