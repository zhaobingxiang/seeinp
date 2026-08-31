package main

import (
	"encoding/json"
	"net/http"
)

// apiResponse 统一响应包装：code=0 表示成功，非 0 为错误码（与 seeinps/seeinpm 约定一致）
type apiResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func writeOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(apiResponse{Code: 0, Message: "ok", Data: data})
}

func writeErr(w http.ResponseWriter, httpStatus, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(apiResponse{Code: code, Message: message})
}

// apiRoot 集中注册部署工具的所有本地 JSON API
type apiRoot struct {
	api *API
}

func newAPIRoot() *apiRoot {
	return &apiRoot{api: NewAPI(&Deps{})}
}

// register 注册走 /api/v1 前缀的后端接口
func (r *apiRoot) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/deploy/validate-auth", r.api.handleValidateAuth)
	mux.HandleFunc("/api/v1/deploy/versions", r.api.handleVersions)
	mux.HandleFunc("/api/v1/deploy/install", r.api.handleInstall)
	mux.HandleFunc("/api/v1/deploy/uninstall", r.api.handleUninstall)
	mux.HandleFunc("/api/v1/deploy/status", r.api.handleDeployStatus)
	mux.HandleFunc("/api/v1/deploy/test-ssh", r.api.handleTestSSH)
	mux.HandleFunc("/api/v1/deploy/privilege", r.api.handlePrivilege)
	mux.HandleFunc("/api/v1/deploy/elevate", r.api.handleElevate)
	mux.HandleFunc("/api/v1/deploy/links", r.api.handleLinks)
	mux.HandleFunc("/api/v1/config/server", r.api.handleServerConfig)
}

// registerRatePostFix 注册一组无版本后缀的便捷别名（均由上文真正实现转发到 /api/v1）
// 保留 /api 作为稳定前缀：见新 API 均挂在 /api/v1，这里不再重复，避免语义分裂。
func (r *apiRoot) registerRatePostFix(mux *http.ServeMux) {}

// API 持有部署流程所需的依赖与一次性运行态
type API struct {
	deps *Deps
}

type Deps struct {
	Logf func(format string, v ...interface{})
}

func NewAPI(deps *Deps) *API {
	if deps.Logf == nil {
		deps.Logf = func(string, ...interface{}) {}
	}
	return &API{deps: deps}
}
