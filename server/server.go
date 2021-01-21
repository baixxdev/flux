package server

import (
	"context"
	"fmt"
	"github.com/bytepowered/flux"
	"github.com/bytepowered/flux/ext"
	"github.com/bytepowered/flux/logger"
	"github.com/bytepowered/flux/webmidware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cast"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const (
	defaultBanner = "Flux-GO // Fast gateway for microservice: dubbo, grpc, http"
	VersionFormat = "Version // git.commit=%s, build.version=%s, build.date=%s"
)

const (
	DefaultHttpHeaderVersion = "X-Version"
)

const (
	HttpWebServerConfigRootName              = "HttpWebServer"
	HttpWebServerConfigKeyFeatureEchoEnable  = "feature-echo-enable"
	HttpWebServerConfigKeyFeatureDebugEnable = "feature-debug-enable"
	HttpWebServerConfigKeyFeatureDebugPort   = "feature-debug-port"
	HttpWebServerConfigKeyRequestLogEnable   = "request-log-enable"
	HttpWebServerConfigKeyAddress            = "address"
	HttpWebServerConfigKeyPort               = "port"
	HttpWebServerConfigKeyTlsCertFile        = "tls-cert-file"
	HttpWebServerConfigKeyTlsKeyFile         = "tls-key-file"
	HttpWebServerConfigKeyManageAddress      = "manage-address"
	HttpWebServerConfigKeyManagePort         = "manage-port"
)

type (
	// Option 配置HttpServeEngine函数
	Option func(engine *HttpServeEngine)
	// VersionLookupFunc Http请求版本查找函数
	VersionLookupFunc func(webc flux.WebContext) string
)

// ServeEngine
type HttpServeEngine struct {
	httpWebServer   flux.WebServer
	manageWebServer flux.WebServer
	responseWriter  flux.ServerResponseWriter
	errorsWriter    flux.ServerErrorsWriter
	ctxHooks        []flux.ServerContextHookFunc
	interceptors    []flux.WebInterceptor
	debugServer     *http.Server
	config          *flux.Configuration
	defaults        map[string]interface{}
	router          *Router
	registry        flux.EndpointRegistry
	versionLookup   VersionLookupFunc
	ctxPool         sync.Pool
	started         chan struct{}
	stopped         chan struct{}
	banner          string
}

// WithServerResponseWriter 用于配置Web服务响应数据输出函数
func WithServerResponseWriter(writer flux.ServerResponseWriter) Option {
	return func(engine *HttpServeEngine) {
		engine.responseWriter = writer
	}
}

// WithServerErrorsWriter 用于配置Web服务错误输出响应数据函数
func WithServerErrorsWriter(writer flux.ServerErrorsWriter) Option {
	return func(engine *HttpServeEngine) {
		engine.errorsWriter = writer
	}
}

// WithServerContextHooks 配置请求Hook函数列表
func WithServerContextHooks(hooks ...flux.ServerContextHookFunc) Option {
	return func(engine *HttpServeEngine) {
		engine.ctxHooks = append(engine.ctxHooks, hooks...)
	}
}

// WithServerWebInterceptors 配置Web服务拦截器列表
func WithServerWebInterceptors(wis ...flux.WebInterceptor) Option {
	return func(engine *HttpServeEngine) {
		engine.interceptors = append(engine.interceptors, wis...)
	}
}

// WithServerWebVersionLookupFunc 配置Web请求版本选择函数
func WithServerWebVersionLookupFunc(fun VersionLookupFunc) Option {
	return func(engine *HttpServeEngine) {
		engine.versionLookup = fun
	}
}

// WithServerDefaults 配置Web服务默认配置
func WithServerDefaults(defaults map[string]interface{}) Option {
	return func(engine *HttpServeEngine) {
		engine.defaults = defaults
	}
}

// WithBanner 配置服务Banner
func WithServerBanner(banner string) Option {
	return func(engine *HttpServeEngine) {
		engine.banner = banner
	}
}

// WithPrepareHooks 配置服务启动预备阶段Hook函数列表
func WithPrepareHooks(hooks ...flux.PrepareHookFunc) Option {
	return func(engine *HttpServeEngine) {
		engine.router.hooks = append(engine.router.hooks, hooks...)
	}
}

func NewHttpServeEngine() *HttpServeEngine {
	return NewHttpServeEngineOverride()
}

func NewHttpServeEngineOverride(overrides ...Option) *HttpServeEngine {
	opts := []Option{WithServerBanner(defaultBanner),
		WithServerResponseWriter(DefaultServerResponseWriter),
		WithServerErrorsWriter(DefaultServerErrorsWriter),
		WithServerWebInterceptors(
			webmidware.NewCORSMiddleware(),
			webmidware.NewRequestIdMiddlewareWithinHeader(),
		),
		WithServerWebVersionLookupFunc(func(webc flux.WebContext) string {
			return webc.HeaderValue(DefaultHttpHeaderVersion)
		}),
		WithServerDefaults(map[string]interface{}{
			HttpWebServerConfigKeyFeatureDebugEnable: false,
			HttpWebServerConfigKeyFeatureDebugPort:   9527,
			HttpWebServerConfigKeyAddress:            "0.0.0.0",
			HttpWebServerConfigKeyPort:               8080,
		})}
	return NewHttpServeEngineWith(DefaultContextFactory, append(opts, overrides...)...)
}

func NewHttpServeEngineWith(factory func() flux.Context, opts ...Option) *HttpServeEngine {
	hse := &HttpServeEngine{
		router:         NewRouter(),
		responseWriter: DefaultServerResponseWriter,
		errorsWriter:   DefaultServerErrorsWriter,
		ctxPool:        sync.Pool{New: func() interface{} { return factory() }},
		ctxHooks:       make([]flux.ServerContextHookFunc, 0, 4),
		interceptors:   make([]flux.WebInterceptor, 0, 4),
		started:        make(chan struct{}),
		stopped:        make(chan struct{}),
		banner:         defaultBanner,
	}
	for _, opt := range opts {
		opt(hse)
	}
	return hse
}

// Prepare Call before init and startup
func (s *HttpServeEngine) Prepare() error {
	return s.router.Prepare()
}

// Initial
func (s *HttpServeEngine) Initial() error {
	// Http server
	s.config = flux.NewConfigurationOf(HttpWebServerConfigRootName)
	s.config.SetDefaults(s.defaults)
	// 创建WebServer
	s.httpWebServer = ext.LoadWebServerFactory()(s.config)
	// 默认必备的WebServer功能
	s.httpWebServer.SetWebErrorHandler(s.defaultServerErrorHandler)
	s.httpWebServer.SetWebNotFoundHandler(s.defaultNotFoundErrorHandler)
	// Manage web server
	s.manageWebServer = ext.LoadWebServerFactory()(s.config)
	s.manageWebServer.SetWebErrorHandler(s.defaultServerErrorHandler)
	s.manageWebServer.SetWebNotFoundHandler(s.defaultNotFoundErrorHandler)
	// 第一优先级的拦截器
	for _, wi := range s.interceptors {
		s.AddWebInterceptor(wi)
	}
	// Endpoint registry
	if registry, config, err := activeEndpointRegistry(); nil != err {
		return err
	} else {
		if err := s.router.InitialHook(registry, config); nil != err {
			return err
		}
		s.registry = registry
	}
	// Echo feature
	if s.config.GetBool(HttpWebServerConfigKeyFeatureEchoEnable) {
		logger.Info("EchoEndpoint register")
		for _, evt := range NewEchoEndpoints() {
			s.HandleHttpEndpointEvent(evt)
		}
	}
	// 管理功能
	s.manageWebServer.AddWebHttpHandler("GET", "/debug/endpoints", NewDebugQueryEndpointHandler())
	s.manageWebServer.AddWebHttpHandler("GET", "/debug/services", NewDebugQueryServiceHandler())
	s.manageWebServer.AddWebHttpHandler("GET", "/debug/metrics", promhttp.Handler())
	return s.router.Initial()
}

func (s *HttpServeEngine) Startup(version flux.BuildInfo) error {
	return s.StartServe(version, s.config)
}

// StartServe server
func (s *HttpServeEngine) StartServe(info flux.BuildInfo, config *flux.Configuration) error {
	s.ensure()
	if err := s.router.Startup(); nil != err {
		return err
	}
	// Http endpoints
	if events, err := s.registry.WatchHttpEndpoints(); nil != err {
		return fmt.Errorf("start registry watching: %w", err)
	} else {
		go func() {
			logger.Info("HttpEndpoint event loop: starting")
			for event := range events {
				s.HandleHttpEndpointEvent(event)
			}
			logger.Info("HttpEndpoint event loop: Stopped")
		}()
	}
	// Backend services
	if events, err := s.registry.WatchBackendServices(); nil != err {
		return fmt.Errorf("start registry watching: %w", err)
	} else {
		go func() {
			logger.Info("BackendService event loop: starting")
			for event := range events {
				s.HandleBackendServiceEvent(event)
			}
			logger.Info("BackendService event loop: Stopped")
		}()
	}
	close(s.started)
	if "" != s.banner {
		logger.Info(s.banner)
	}
	logger.Infof(VersionFormat, info.CommitId, info.Version, info.Date)
	startServer := func(server flux.WebServer, name, address string, port int, tlscert, tlskey string) error {
		addr := fmt.Sprintf("%s:%d", address, port)
		logger.Infow(name+" starting", "address", address, "cert", tlscert, "key", tlskey)
		err := server.StartTLS(addr, tlscert, tlskey)
		if nil != err {
			logger.Errorw(name+" start failed", "error", err)
		}
		return err
	}
	// Start Servers
	go startServer(s.manageWebServer, "Manage web server",
		s.config.GetString(HttpWebServerConfigKeyManageAddress), s.config.GetInt(HttpWebServerConfigKeyManagePort),
		"", "",
	)
	tlsKeyFile := config.GetString(HttpWebServerConfigKeyTlsKeyFile)
	tlsCertFile := config.GetString(HttpWebServerConfigKeyTlsCertFile)
	return startServer(s.httpWebServer, "Http web server",
		config.GetString(HttpWebServerConfigKeyAddress), config.GetInt(HttpWebServerConfigKeyManagePort),
		tlsCertFile, tlsKeyFile,
	)
}

func (s *HttpServeEngine) HandleMultiEndpointRequest(webc flux.WebContext, endpoints *MultiEndpoint, tracing bool) error {
	version := s.versionLookup(webc)
	endpoint, found := endpoints.FindByVersion(version)
	requestId := cast.ToString(webc.GetValue(flux.HeaderXRequestId))
	if found {
		return s.HandleEndpointRequest(webc, requestId, endpoint)
	}
	if tracing {
		url, _ := webc.RequestURL()
		logger.Trace(requestId).Infow("HttpServeEngine route not-found",
			"http-pattern", []string{webc.Method(), webc.RequestURI(), url.Path},
			"http-version", version,
		)
	}
	return flux.ErrRouteNotFound
}

func (s *HttpServeEngine) HandleEndpointRequest(webc flux.WebContext, requestId string, endpoint *flux.Endpoint) error {
	defer func() {
		if r := recover(); r != nil {
			trace := logger.Trace(requestId)
			if err, ok := r.(error); ok {
				trace.Errorw("HttpServeEngine panics", "error", err)
			} else {
				trace.Errorw("HttpServeEngine panics", "recover", r)
			}
			trace.Error(string(debug.Stack()))
		}
	}()
	ctxw := s.acquireContext(requestId, webc, endpoint)
	defer s.releaseContext(ctxw)
	// Route call
	logger.TraceContext(ctxw).Infow("HttpServeEngine route start")
	endcall := func(code int, start time.Time) {
		logger.TraceContext(ctxw).Infow("HttpServeEngine route end",
			"metric", ctxw.LoadMetrics(),
			"elapses", time.Since(start).String(), "response.code", code)
	}
	start := time.Now()
	// Context hook
	for _, hook := range s.ctxHooks {
		hook(webc, ctxw)
	}
	// Route and response
	response := ctxw.Response()
	if err := s.router.Route(ctxw); nil != err {
		defer endcall(err.StatusCode, start)
		logger.TraceContext(ctxw).Errorw("HttpServeEngine route error", "error", err)
		err.MergeHeader(response.HeaderValues())
		return err
	} else {
		defer endcall(response.StatusCode(), start)
		return s.responseWriter(webc, requestId, response.HeaderValues(), response.StatusCode(), response.Body())
	}
}

func (s *HttpServeEngine) HandleBackendServiceEvent(event flux.BackendServiceEvent) {
	service := event.Service
	initArguments(service.Arguments)
	switch event.EventType {
	case flux.EventTypeAdded:
		logger.Infow("New service",
			"service-id", service.ServiceId, "alias-id", service.AliasId)
		ext.StoreBackendService(service)
		if "" != service.AliasId {
			ext.StoreBackendServiceById(service.AliasId, service)
		}
	case flux.EventTypeUpdated:
		logger.Infow("Update service",
			"service-id", service.ServiceId, "alias-id", service.AliasId)
		ext.StoreBackendService(service)
		if "" != service.AliasId {
			ext.StoreBackendServiceById(service.AliasId, service)
		}
	case flux.EventTypeRemoved:
		logger.Infow("Delete service",
			"service-id", service.ServiceId, "alias-id", service.AliasId)
		ext.RemoveBackendService(service.ServiceId)
		if "" != service.AliasId {
			ext.RemoveBackendService(service.AliasId)
		}
	}
}

func (s *HttpServeEngine) HandleHttpEndpointEvent(event flux.HttpEndpointEvent) {
	method := strings.ToUpper(event.Endpoint.HttpMethod)
	// Check http method
	if !isAllowedHttpMethod(method) {
		logger.Warnw("Unsupported http method", "method", method, "pattern", event.Endpoint.HttpPattern)
		return
	}
	pattern := event.Endpoint.HttpPattern
	// Refresh endpoint
	endpoint := event.Endpoint
	initArguments(endpoint.Service.Arguments)
	initArguments(endpoint.Permission.Arguments)
	if endpoint.AttrByTag(flux.EndpointAttrTagManaged).IsValid() {
		s.registerManageEndpoint(method, pattern, event.EventType, &endpoint)
	} else {
		routeKey := fmt.Sprintf("%s#%s", method, pattern)
		s.registerServeEndpoint(routeKey, method, pattern, event.EventType, &endpoint)
	}
}

// 内部管理接口，不支持多版本
func (s *HttpServeEngine) registerManageEndpoint(method, pattern string, event flux.EventType, endpoint *flux.Endpoint) {
	logger.Infow("Register managed http handler", "method", method, "pattern", pattern)
	switch event {
	case flux.EventTypeAdded, flux.EventTypeUpdated:
		s.manageWebServer.AddWebHandler(method, pattern, func(webc flux.WebContext) error {
			requestId := cast.ToString(webc.GetValue(flux.HeaderXRequestId))
			return s.HandleEndpointRequest(webc, requestId, endpoint)
		})
	case flux.EventTypeRemoved:
		// TODO WebServer需要支持移除Handler实现
	}
	return
}

// 对外服务端口支持多版本
func (s *HttpServeEngine) registerServeEndpoint(routeKey string, method, pattern string, event flux.EventType, endpoint *flux.Endpoint) {
	multi, toRegister := s.selectMultiEndpoint(routeKey, endpoint)
	switch event {
	case flux.EventTypeAdded:
		logger.Infow("New endpoint", "version", endpoint.Version, "method", method, "pattern", pattern)
		multi.Update(endpoint.Version, endpoint)
		if toRegister {
			logger.Infow("Register http handler", "method", method, "pattern", pattern)
			s.httpWebServer.AddWebHandler(method, pattern, s.newWrappedEndpointHandler(multi))
		}
	case flux.EventTypeUpdated:
		logger.Infow("Update endpoint", "version", endpoint.Version, "method", method, "pattern", pattern)
		multi.Update(endpoint.Version, endpoint)
	case flux.EventTypeRemoved:
		logger.Infow("Delete endpoint", "method", method, "pattern", pattern)
		multi.Delete(endpoint.Version)
		// TODO WebServer需要支持移除Handler实现
	}
}

// Shutdown to cleanup resources
func (s *HttpServeEngine) Shutdown(ctx context.Context) error {
	logger.Info("HttpServeEngine shutdown...")
	defer close(s.stopped)
	_ = s.manageWebServer.Shutdown(ctx)
	if err := s.httpWebServer.Shutdown(ctx); nil != err {
		logger.Warnw("HttpServeEngine shutdown http server", "error", err)
	}
	return s.router.Shutdown(ctx)
}

// StateStarted 返回一个Channel。当服务启动完成时，此Channel将被关闭。
func (s *HttpServeEngine) StateStarted() <-chan struct{} {
	return s.started
}

// StateStopped 返回一个Channel。当服务停止后完成时，此Channel将被关闭。
func (s *HttpServeEngine) StateStopped() <-chan struct{} {
	return s.stopped
}

// HttpConfig return Http server configuration
func (s *HttpServeEngine) HttpConfig() *flux.Configuration {
	return s.config
}

// AddWebInterceptor 添加Http前拦截器。将在Http被路由到对应Handler之前执行
func (s *HttpServeEngine) AddWebInterceptor(m flux.WebInterceptor) {
	s.ensure().httpWebServer.AddWebInterceptor(m)
}

// AddWebHandler 添加Http处理接口。
func (s *HttpServeEngine) AddWebHandler(method, pattern string, h flux.WebHandler, m ...flux.WebInterceptor) {
	s.ensure().httpWebServer.AddWebHandler(method, pattern, h, m...)
}

// AddWebHttpHandler 添加Http处理接口。
func (s *HttpServeEngine) AddWebHttpHandler(method, pattern string, h http.Handler, m ...func(http.Handler) http.Handler) {
	s.ensure().httpWebServer.AddWebHttpHandler(method, pattern, h, m...)
}

// SetWebNotFoundHandler 设置Http路由失败的处理接口
func (s *HttpServeEngine) SetWebNotFoundHandler(nfh flux.WebHandler) {
	s.ensure().httpWebServer.SetWebNotFoundHandler(nfh)
}

// WebServer 返回WebServer实例
func (s *HttpServeEngine) WebServer() flux.WebServer {
	return s.ensure().httpWebServer
}

// ManageWebServer 返回管理端的WebServer实例，以及实例是否有效
func (s *HttpServeEngine) ManageWebServer() flux.WebServer {
	return s.manageWebServer
}

// AddServerContextHookFunc 添加Http与Flux的Context桥接函数
func (s *HttpServeEngine) AddServerContextHookFunc(f flux.ServerContextHookFunc) {
	s.ctxHooks = append(s.ctxHooks, f)
}

func (s *HttpServeEngine) newWrappedEndpointHandler(endpoint *MultiEndpoint) flux.WebHandler {
	enabled := s.config.GetBool(HttpWebServerConfigKeyRequestLogEnable)
	return func(webc flux.WebContext) error {
		return s.HandleMultiEndpointRequest(webc, endpoint, enabled)
	}
}

func (s *HttpServeEngine) selectMultiEndpoint(routeKey string, endpoint *flux.Endpoint) (*MultiEndpoint, bool) {
	if mve, ok := SelectMultiEndpoint(routeKey); ok {
		return mve, false
	} else {
		return RegisterMultiEndpoint(routeKey, endpoint), true
	}
}

func (s *HttpServeEngine) acquireContext(id string, webc flux.WebContext, endpoint *flux.Endpoint) *DefaultContext {
	ctx := s.ctxPool.Get().(*DefaultContext)
	ctx.Reattach(id, webc, endpoint)
	return ctx
}

func (s *HttpServeEngine) releaseContext(context *DefaultContext) {
	context.Release()
	s.ctxPool.Put(context)
}

func (s *HttpServeEngine) ensure() *HttpServeEngine {
	if s.httpWebServer == nil {
		logger.Panicf("Call must after InitialServer()")
	}
	return s
}

func (s *HttpServeEngine) defaultNotFoundErrorHandler(webc flux.WebContext) error {
	return &flux.ServeError{
		StatusCode: flux.StatusNotFound,
		ErrorCode:  flux.ErrorCodeRequestNotFound,
		Message:    flux.ErrorMessageWebServerRequestNotFound,
	}
}

func (s *HttpServeEngine) defaultServerErrorHandler(err error, webc flux.WebContext) {
	if err == nil {
		return
	}
	// Http中间件等返回InvokeError错误
	serve, ok := err.(*flux.ServeError)
	if !ok {
		serve = &flux.ServeError{
			StatusCode: flux.StatusServerError,
			ErrorCode:  flux.ErrorCodeGatewayInternal,
			Message:    err.Error(),
			Header:     http.Header{},
			Internal:   err,
		}
	}
	requestId := cast.ToString(webc.GetValue(flux.HeaderXRequestId))
	if err := s.errorsWriter(webc, requestId, serve.Header, serve); nil != err {
		logger.Trace(requestId).Errorw("HttpServeEngine http response error", "error", err)
	}
}

func activeEndpointRegistry() (flux.EndpointRegistry, *flux.Configuration, error) {
	config := flux.NewConfigurationOf(flux.KeyConfigRootEndpointRegistry)
	config.SetDefault(flux.KeyConfigEndpointRegistryProto, ext.EndpointRegistryProtoDefault)
	registryProto := config.GetString(flux.KeyConfigEndpointRegistryProto)
	logger.Infow("Active endpoint registry", "registry-proto", registryProto)
	if factory, ok := ext.LoadEndpointRegistryFactory(registryProto); !ok {
		return nil, config, fmt.Errorf("EndpointRegistryFactory not found, proto: %s", registryProto)
	} else {
		return factory(), config, nil
	}
}

func isAllowedHttpMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPut,
		http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodTrace:
		// Allowed
		return true
	default:
		// http.MethodConnect, and Others
		logger.Errorw("Ignore unsupported http method:", "method", method)
		return false
	}
}

func initArguments(args []flux.Argument) {
	for i := range args {
		args[i].ValueResolver = ext.LoadMTValueResolver(args[i].Class)
		args[i].LookupFunc = ext.LoadArgumentLookupFunc()
		initArguments(args[i].Fields)
	}
}
