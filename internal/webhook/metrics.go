package webhook

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	resultMutated   = "mutated"
	resultUnchanged = "unchanged"
	resultIgnored   = "ignored"
	resultDenied    = "denied"
	resultError     = "error"
)

var requestResults = []string{
	resultMutated,
	resultUnchanged,
	resultIgnored,
	resultDenied,
	resultError,
}

type Observer interface {
	ObserveRequest(result string, duration time.Duration)
}

type noopObserver struct{}

func (noopObserver) ObserveRequest(result string, duration time.Duration) {}

type PrometheusObserver struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

func NewPrometheusObserver(registerer prometheus.Registerer) *PrometheusObserver {
	observer := &PrometheusObserver{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "topo_rdma",
			Subsystem: "webhook",
			Name:      "requests_total",
			Help:      "total number of pod admission requests",
		}, []string{"result"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "topo_rdma",
			Subsystem: "webhook",
			Name:      "request_duration_seconds",
			Help:      "duration of pod admission request processing in seconds",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result"}),
	}
	registerer.MustRegister(observer.requestsTotal, observer.requestDuration)
	for _, result := range requestResults {
		observer.requestsTotal.WithLabelValues(result)
		observer.requestDuration.WithLabelValues(result)
	}
	return observer
}

// ObserveRequest 观察request结果 记录并更新metrics指标
func (o *PrometheusObserver) ObserveRequest(result string, duration time.Duration) {
	o.requestsTotal.WithLabelValues(result).Inc()
	o.requestDuration.WithLabelValues(result).Observe(duration.Seconds())
}
