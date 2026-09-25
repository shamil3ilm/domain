// Package metrics owns the Prometheus registry and the concrete series
// exported at /metrics. Callers Inc/Observe on the exported vars; the
// registry is exposed as an http.Handler for the API to mount.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the process-wide registry. It is populated by the exported vars
// below at init time. Kept private so callers can't accidentally register a
// second copy of the same metric family and blow up at scrape time.
var registry = prometheus.NewRegistry()

// DNSQueriesTotal counts every DNS query the server received, labelled by
// query type (A, AAAA, TXT, …) and the response code we produced (NOERROR,
// NXDOMAIN, REFUSED, SERVFAIL, DROPPED for rate-limit or ACL drops).
var DNSQueriesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "privatedns_dns_queries_total",
		Help: "Total DNS queries handled, by query type and response code.",
	},
	[]string{"type", "rcode"},
)

// DNSQueryDuration is the wall-clock time from packet arrival to
// response-written, labelled by query type. Bucket boundaries target the
// realistic latency envelope of an authoritative + local-forwarder mix:
// authoritative answers are sub-millisecond, forwarded queries land in the
// tens of milliseconds.
var DNSQueryDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "privatedns_dns_query_duration_seconds",
		Help:    "Time spent handling a DNS query, seconds.",
		Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5},
	},
	[]string{"type"},
)

// DNSRateLimitDrops is bumped every time the token-bucket rejects a query
// (separate from ACL-refused). Not labelled — the source IP is not
// cardinality-safe.
var DNSRateLimitDrops = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "privatedns_dns_ratelimit_drops_total",
		Help: "Queries dropped by the per-source-IP rate limiter.",
	},
)

// DNSACLRefusals counts queries refused for being outside the query or
// recursion ACL.
var DNSACLRefusals = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "privatedns_dns_acl_refusals_total",
		Help: "Queries refused by ACL, by reason (query|recursion).",
	},
	[]string{"reason"},
)

// HTTPRequestsTotal counts management API requests, labelled by method,
// route pattern (not raw path — cardinality) and status code.
var HTTPRequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "privatedns_http_requests_total",
		Help: "Total HTTP requests to the management API.",
	},
	[]string{"method", "route", "status"},
)

// HTTPRequestDuration mirrors the counter with a latency histogram.
var HTTPRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "privatedns_http_request_duration_seconds",
		Help:    "Time spent handling an HTTP request, seconds.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
	[]string{"method", "route"},
)

// BuildInfo is a gauge that always reads 1; its labels carry the version.
// Convention borrowed from other exporters — makes Grafana version overlays
// trivial ("group by version").
var BuildInfo = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "privatedns_build_info",
		Help: "Constant 1 gauge whose labels describe the running build.",
	},
	[]string{"version"},
)

func init() {
	registry.MustRegister(
		DNSQueriesTotal,
		DNSQueryDuration,
		DNSRateLimitDrops,
		DNSACLRefusals,
		HTTPRequestsTotal,
		HTTPRequestDuration,
		BuildInfo,
		// Runtime metrics come for free — GC, goroutine count, heap, etc.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// SetBuildInfo sets the version label on build_info to 1. Callers call this
// once at startup with the compile-time version constant.
func SetBuildInfo(version string) {
	BuildInfo.WithLabelValues(version).Set(1)
}

// Handler returns the http.Handler that serves /metrics in Prometheus text
// exposition format.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
