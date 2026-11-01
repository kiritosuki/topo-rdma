package agent

import (
	"sync/atomic"
	"time"

	"github.com/kiritosuki/topo-rdma/internal/topology"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricResultSuccess = "success"
	metricResultError   = "error"
)

type Observer interface {
	ObservePublication(duration time.Duration, snapshot topology.Snapshot, err error)
}

type noopObserver struct{}

func (noopObserver) ObservePublication(duration time.Duration, snapshot topology.Snapshot, err error) {
}

type PrometheusObserver struct {
	publicationTotal    *prometheus.CounterVec
	publicationDuration *prometheus.HistogramVec
	numaNodes           prometheus.Gauge
	gpus                prometheus.Gauge
	rdmaDevices         prometheus.Gauge
	lastSuccess         prometheus.Gauge
	ready               atomic.Bool
}

func NewPrometheusObserver(registerer prometheus.Registerer) *PrometheusObserver {
	observer := &PrometheusObserver{
		publicationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "publication_total",
			Help:      "total number of node topology publication attempts",
		}, []string{"result"}),
		publicationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "publication_duration_seconds",
			Help:      "duration of node topology publication per round",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result"}),
		numaNodes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "numa_nodes",
			Help:      "number of NUMA nodes in the most recently published topology",
		}),
		gpus: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "gpus",
			Help:      "number of gpus in the most recently published topology",
		}),
		rdmaDevices: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "rdma_devices",
			Help:      "number of RDMA devices in the most recently published topology",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "topo_rdma",
			Subsystem: "agent",
			Name:      "last_success_timestamp_seconds",
			Help:      "Unix timestamp of the last successful node topology publication",
		}),
	}
	registerer.MustRegister(
		observer.publicationTotal,
		observer.publicationDuration,
		observer.numaNodes,
		observer.gpus,
		observer.rdmaDevices,
		observer.lastSuccess,
	)
	return observer
}

func (o *PrometheusObserver) ObservePublication(duration time.Duration, snapshot topology.Snapshot, err error) {
	result := metricResult(err)
	o.publicationTotal.WithLabelValues(result).Inc()
	o.publicationDuration.WithLabelValues(result).Observe(duration.Seconds())
	o.ready.Store(err == nil)
	if err != nil {
		return
	}
	numaNodes, gpus, rdmaDevices := snapshotCounts(snapshot)
	o.numaNodes.Set(float64(numaNodes))
	o.gpus.Set(float64(gpus))
	o.rdmaDevices.Set(float64(rdmaDevices))
	o.lastSuccess.SetToCurrentTime()
}

func metricResult(err error) string {
	if err != nil {
		return metricResultError
	}
	return metricResultSuccess
}

// snapshotCounts 返回 (numa节点数, gpu数, rdma数)
func snapshotCounts(snapshot topology.Snapshot) (int, int, int) {
	gpus := len(snapshot.UnassignedGPUs)
	rdmaDevices := len(snapshot.UnassignedRDMA)
	for _, numaNode := range snapshot.NUMANodes {
		gpus += len(numaNode.GPUs)
		rdmaDevices += len(numaNode.RDMADevices)
	}
	return len(snapshot.NUMANodes), gpus, rdmaDevices
}
