package controllers

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	metricResultSuccess = "success"
	metricResultError   = "error"
)

var freshnessStatuses = []string{"true", "false", "unknown"}

type Observer interface {
	ObserveReconcile(name string, duration time.Duration, status *metav1.ConditionStatus, err error)
	ForgetNode(name string)
}

type noopObserver struct{}

func (noopObserver) ObserveReconcile(name string, duration time.Duration, status *metav1.ConditionStatus, err error) {
}

func (noopObserver) ForgetNode(name string) {}

type PrometheusObserver struct {
	reconcileTotal    *prometheus.CounterVec
	reconcileDuration *prometheus.HistogramVec
	freshnessTotal    *prometheus.CounterVec
	nodeFreshness     *prometheus.GaugeVec
}

func NewPrometheusObserver(registerer prometheus.Registerer) *PrometheusObserver {
	observer := &PrometheusObserver{
		reconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "topo_rdma",
			Subsystem: "controller",
			Name:      "reconcile_total",
			Help:      "total number of NodeTopology reconciliation attempts",
		}, []string{"result"}),
		reconcileDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "topo_rdma",
			Subsystem: "controller",
			Name:      "reconcile_duration_seconds",
			Help:      "Duration of each NodeTopology reconciliation in seconds",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result"}),
		freshnessTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "topo_rdma",
			Subsystem: "controller",
			Name:      "freshness_evaluation_total",
			Help:      "total number of topology freshness checks by status",
		}, []string{"status"}),
		nodeFreshness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "topo_rdma",
			Subsystem: "controller",
			Name:      "node_topology_fresh",
			Help:      "current topology freshness status for each node (1 if active, 0 otherwise)",
		}, []string{"node", "status"}),
	}
	registerer.MustRegister(
		observer.reconcileTotal,
		observer.reconcileDuration,
		observer.freshnessTotal,
		observer.nodeFreshness,
	)
	return observer
}

// ObserveReconcile 观察协调结果 记录并更新metrics指标
func (o *PrometheusObserver) ObserveReconcile(
	node string,
	duration time.Duration,
	status *metav1.ConditionStatus,
	err error,
) {
	result := metricResult(err)
	o.reconcileTotal.WithLabelValues(result).Inc()
	o.reconcileDuration.WithLabelValues(result).Observe(duration.Seconds())
	if status == nil {
		return
	}
	statusLabel := conditionStatusLabel(*status)
	o.freshnessTotal.WithLabelValues(statusLabel).Inc()
	for _, candidate := range freshnessStatuses {
		value := 0.0
		if candidate == statusLabel {
			value = 1
		}
		o.nodeFreshness.WithLabelValues(node, candidate).Set(value)
	}
}

// ForgetNode 节点被删除后 用于清理该节点在prometheus中的指标数据
func (o *PrometheusObserver) ForgetNode(node string) {
	for _, status := range freshnessStatuses {
		o.nodeFreshness.DeleteLabelValues(node, status)
	}
}

func metricResult(err error) string {
	if err != nil {
		return metricResultError
	}
	return metricResultSuccess
}

// conditionStatusLabel 把conditionStatus转成字符串 方便作label使用
func conditionStatusLabel(status metav1.ConditionStatus) string {
	switch status {
	case metav1.ConditionTrue:
		return "true"
	case metav1.ConditionFalse:
		return "false"
	default:
		return "unknown"
	}
}
