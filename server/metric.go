package server

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricNamespace = "flux"
	metricSubsystem = "endpoint"
	metricBuckets   = []float64{
		0.0005,
		0.001, // 1ms
		0.002,
		0.005,
		0.01, // 10ms
		0.02,
		0.05,
		0.1, // 100 ms
		0.2,
		0.5,
		1.0, // 1s
		2.0,
		5.0,
		10.0, // 10s
		15.0,
		20.0,
		30.0,
	}
)

type EndpointMetrics struct {
	AccessCounter *prometheus.CounterVec
	ErrorCounter  *prometheus.CounterVec
	RouteDuration *prometheus.HistogramVec
}

func NewEndpointMetrics() *EndpointMetrics {
	return &EndpointMetrics{
		// Endpoint访问次数
		AccessCounter: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "access_total",
			Help:      "Number of endpoint access",
		}, []string{"ProtoName", "Interface", "Method"}),
		// Endpoint访问错误统计
		ErrorCounter: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "error_total",
			Help:      "Number of endpoint access errors",
		}, []string{"ProtoName", "Interface", "Method", "ErrorCode"}),
		// Endpoint访问耗时统计
		RouteDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "route_duration",
			Help:      "Spend time by processing an endpoint",
			Buckets:   metricBuckets,
		}, []string{"ComponentType", "TypeId"}),
	}
}
