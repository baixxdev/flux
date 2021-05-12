package filter

import (
	"errors"
	"fmt"
	"github.com/bytepowered/flux"
	"github.com/bytepowered/flux/ext"
	"github.com/bytepowered/flux/logger"
	"github.com/bytepowered/flux/toolkit"
	"github.com/bytepowered/flux/transporter"
	"net/http"
	"time"
)

const (
	TypeIdPermissionV2Filter = "permission_filter"
)

type (
	// PermissionReport 权限验证结果报告
	PermissionReport struct {
		StatusCode int    `json:"statusCode"`
		Success    bool   `json:"success"`
		ErrorCode  string `json:"errorCode"`
		Message    string `json:"message"`
	}
	// PermissionVerifyFunc 权限验证
	// @return pass 对当前请求的权限验证是否通过；
	// @return err 如果验证过程发生错误，返回error；
	PermissionVerifyFunc func(services []flux.Service, ctx *flux.Context) (report PermissionReport, err error)
)

// PermissionConfig 权限配置
type PermissionConfig struct {
	SkipFunc   flux.FilterSkipper
	VerifyFunc PermissionVerifyFunc
}

func NewPermissionVerifyReport(success bool, errorCode, message string) PermissionReport {
	return PermissionReport{
		StatusCode: flux.StatusUnauthorized,
		Success:    success,
		ErrorCode:  errorCode,
		Message:    message,
	}
}

func NewPermissionFilter(c PermissionConfig) *PermissionFilter {
	return &PermissionFilter{
		Configs: c,
	}
}

// PermissionFilter 提供基于Endpoint.Permission元数据的权限验证
type PermissionFilter struct {
	Disabled bool
	Configs  PermissionConfig
}

func (p *PermissionFilter) Init(config *flux.Configuration) error {
	config.SetDefaults(map[string]interface{}{
		ConfigKeyDisabled: false,
	})
	p.Disabled = config.GetBool(ConfigKeyDisabled)
	if p.Disabled {
		logger.Info("Endpoint PermissionFilter was DISABLED!!")
		return nil
	}
	if toolkit.IsNil(p.Configs.SkipFunc) {
		p.Configs.SkipFunc = func(_ *flux.Context) bool {
			return false
		}
	}
	if toolkit.IsNil(p.Configs.VerifyFunc) {
		return fmt.Errorf("PermissionFilter.PermissionVerifyFunc is nil")
	}
	return nil
}

func (*PermissionFilter) FilterId() string {
	return TypeIdPermissionV2Filter
}

func (p *PermissionFilter) DoFilter(next flux.FilterInvoker) flux.FilterInvoker {
	if p.Disabled {
		return next
	}
	return func(ctx *flux.Context) *flux.ServeError {
		if p.Configs.SkipFunc(ctx) {
			return next(ctx)
		}
		// 没有任何权限校验定义
		endpoint := ctx.Endpoint()
		permissions := endpoint.AttrPermissions()
		services := make([]flux.Service, 0, len(permissions))
		for _, id := range permissions {
			if srv, ok := ext.ServiceByID(id); ok {
				services = append(services, srv)
			} else {
				return &flux.ServeError{
					StatusCode: flux.StatusServerError,
					ErrorCode:  flux.ErrorCodeGatewayInternal,
					Message:    flux.ErrorMessagePermissionServiceNotFound,
					CauseError: errors.New("permission.service not found, id: " + id),
				}
			}
		}
		report, err := p.Configs.VerifyFunc(services, ctx)
		ctx.AddMetric(p.FilterId(), time.Since(ctx.StartAt()))
		if nil != err {
			if serr, ok := err.(*flux.ServeError); ok {
				return serr
			}
			return &flux.ServeError{
				StatusCode: http.StatusForbidden,
				ErrorCode:  flux.ErrorCodeGatewayInternal,
				Message:    flux.ErrorMessagePermissionVerifyError,
				CauseError: err,
			}
		}
		if !report.Success {
			return &flux.ServeError{
				StatusCode: EnsurePermissionStatusCode(report.StatusCode),
				ErrorCode:  EnsurePermissionErrorCode(report.ErrorCode),
				Message:    EnsurePermissionMessage(report.Message),
			}
		}
		return next(ctx)
	}
}

// InvokeCodec 执行权限验证的后端服务，获取响应结果；
func (p *PermissionFilter) InvokeCodec(ctx *flux.Context, service flux.Service) (*flux.ResponseBody, *flux.ServeError) {
	return transporter.DoInvokeCodec(ctx, service)
}

func EnsurePermissionStatusCode(status int) int {
	if status < 100 {
		return http.StatusForbidden
	}
	return status
}

func EnsurePermissionErrorCode(code string) string {
	if "" == code {
		return flux.ErrorCodePermissionDenied
	}
	return code
}

func EnsurePermissionMessage(message string) string {
	if "" == message {
		return flux.ErrorMessagePermissionAccessDenied
	}
	return message
}
