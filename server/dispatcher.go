package server

import (
	"context"
	"fmt"
	"github.com/bytepowered/flux"
	"github.com/bytepowered/flux/ext"
	"github.com/bytepowered/flux/logger"
	"github.com/prometheus/client_golang/prometheus"
	"reflect"
	"time"
)

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		metrics: NewMetrics(),
	}
}

type Dispatcher struct {
	metrics *Metrics
}

func (d *Dispatcher) Init() error {
	logger.Info("SERVER:EVENT:DISPATCHER:INIT")
	// Transporter
	for proto, transporter := range ext.Transporters() {
		ns := flux.NamespaceTransporters + "." + proto
		logger.Infow("Load transporter", "proto", proto, "type", reflect.TypeOf(transporter), "config-ns", ns)
		if err := d.AddInitHook(transporter, flux.NewConfiguration(ns)); nil != err {
			return err
		}
	}
	// 手动注册的单实例Filters
	for _, filter := range append(ext.GlobalFilters(), ext.SelectiveFilters()...) {
		ns := filter.FilterId()
		logger.Infow("Load static-filter", "type", reflect.TypeOf(filter), "config-ns", ns)
		config := flux.NewConfiguration(ns)
		if IsDisabled(config) {
			logger.Infow("Set static-filter DISABLED", "filter-id", filter.FilterId())
			continue
		}
		if err := d.AddInitHook(filter, config); nil != err {
			return err
		}
	}
	// 加载和注册，动态多实例Filter
	dynFilters, err := dynamicFilters()
	if nil != err {
		return err
	}
	for _, item := range dynFilters {
		filter := item.Factory()
		logger.Infow("Load dynamic-filter", "filter-id", item.Id, "type-id", item.TypeId, "type", reflect.TypeOf(filter))
		if IsDisabled(item.Config) {
			logger.Infow("Set dynamic-filter DISABLED", "filter-id", item.Id, "type-id", item.TypeId)
			continue
		}
		if err := d.AddInitHook(filter, item.Config); nil != err {
			return err
		}
		if filter, ok := filter.(flux.Filter); ok {
			ext.AddSelectiveFilter(filter)
		}
	}
	return nil
}

func (d *Dispatcher) AddInitHook(ref interface{}, config *flux.Configuration) error {
	if init, ok := ref.(flux.Initializer); ok {
		if err := init.Init(config); nil != err {
			return err
		}
	}
	ext.AddHookFunc(ref)
	return nil
}

func (d *Dispatcher) Startup() error {
	for _, startup := range sortedStartup(ext.StartupHooks()) {
		if err := startup.Startup(); nil != err {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) Shutdown(ctx context.Context) error {
	for _, shutdown := range sortedShutdown(ext.ShutdownHooks()) {
		if err := shutdown.Shutdown(ctx); nil != err {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) Route(ctx *flux.Context) *flux.ServeError {
	// 统计异常
	doMetricEndpointFunc := func(err *flux.ServeError) *flux.ServeError {
		// Access Counter: ProtoName, Interface, Method
		service := ctx.Service()
		proto, uri, method := service.RpcProto(), service.Interface, service.Method
		d.metrics.EndpointAccess.WithLabelValues(proto, uri, method).Inc()
		if nil != err {
			// Error Counter: ProtoName, Interface, Method, ErrorCode
			d.metrics.EndpointError.WithLabelValues(proto, uri, method, err.GetErrorCode()).Inc()
		}
		return err
	}
	// Metric: Route
	defer func() {
		ctx.AddMetric("route", time.Since(ctx.StartAt()))
	}()
	// Select filters
	selective := make([]flux.Filter, 0, 16)
	for _, selector := range ext.FilterSelectors() {
		if selector.Activate(ctx) {
			selective = append(selective, selector.DoSelect(ctx)...)
		}
	}
	ctx.AddMetric("selector", time.Since(ctx.StartAt()))
	transport := func(ctx *flux.Context) *flux.ServeError {
		select {
		case <-ctx.Context().Done():
			return &flux.ServeError{
				StatusCode: flux.StatusOK,
				ErrorCode:  "ROUTE:TRANSPORT/B:CANCELED",
				CauseError: ctx.Context().Err(),
			}
		default:
			break
		}
		defer func() {
			ctx.AddMetric("transporter", time.Since(ctx.StartAt()))
		}()
		proto := ctx.Service().RpcProto()
		transporter, ok := ext.TransporterByProto(proto)
		if !ok {
			logger.TraceId(ctx).Errorw("SERVER:ROUTE:UNSUPPORTED_PROTOCOL",
				"proto", proto, "service", ctx.Endpoint().Service)
			return &flux.ServeError{
				StatusCode: flux.StatusNotFound,
				ErrorCode:  flux.ErrorCodeRequestNotFound,
				Message:    fmt.Sprintf("ROUTE:UNKNOWN_PROTOCOL:%s", proto),
			}
		}
		// Transporter exchange
		timer := prometheus.NewTimer(d.metrics.RouteDuration.WithLabelValues("Transporter", proto))
		transporter.Transport(ctx)
		timer.ObserveDuration()
		return nil
	}
	// Walk filters
	filters := append(ext.GlobalFilters(), selective...)
	return doMetricEndpointFunc(d.walk(transport, filters)(ctx))
}

func (d *Dispatcher) walk(next flux.FilterInvoker, filters []flux.Filter) flux.FilterInvoker {
	for i := len(filters) - 1; i >= 0; i-- {
		next = filters[i].DoFilter(next)
	}
	return next
}
