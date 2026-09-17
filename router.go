package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Router routes commandcode-owned models to this executor.
type Router struct {
	cfg *pluginConfig
}

func NewRouter(cfg *pluginConfig) *Router { return &Router{cfg: cfg} }

func (r *Router) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	if !r.owned(req) {
		return pluginapi.ModelRouteResponse{}, nil
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "commandcode provider plugin",
	}, nil
}

func (r *Router) owned(req pluginapi.ModelRouteRequest) bool {
	if strings.HasPrefix(strings.TrimSpace(req.RequestedModel), Provider+"/") {
		return true
	}
	set := r.cfg.modelSet()
	for _, candidate := range []string{req.RequestedModel, modelFromBody(req.Body)} {
		if candidate == "" {
			continue
		}
		if _, ok := set[normalizeModel(candidate)]; ok {
			return true
		}
	}
	return false
}

func modelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var out struct {
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(body, &out); err != nil {
		return ""
	}
	return out.Model
}
