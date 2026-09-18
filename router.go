package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Router routes commandcode-owned models to this executor.
type Router struct {
	cfg    *pluginConfig
	models *ModelProvider
}

func NewRouter(cfg *pluginConfig, models ...*ModelProvider) *Router {
	r := &Router{cfg: cfg}
	if len(models) > 0 {
		r.models = models[0]
	}
	return r
}

func (r *Router) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	model := strings.TrimSpace(req.RequestedModel)
	if model == "" {
		model = strings.TrimSpace(modelFromBody(req.Body))
	}
	if !r.owned(model) {
		return pluginapi.ModelRouteResponse{}, nil
	}
	// Once the host has a Command Code auth record, let its normal model/auth
	// resolver handle both bare and namespaced requests. That path carries the
	// selected auth into the executor and lets Command Code compete with native
	// providers. A config-only installation has no host auth candidate, so keep
	// the direct executor fallback instead of making the model unavailable.
	if r.cfg != nil && r.cfg.sharedScheduling() && hasProvider(req.AvailableProviders, Provider) {
		if hasCommandCodePrefix(model) {
			requested := stripCommandCodePrefix(model)
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      Provider,
				TargetModel: r.publicModelName(requested),
				Reason:      "commandcode provider auth route",
			}, nil
		}
		return pluginapi.ModelRouteResponse{}, nil
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "commandcode provider plugin",
	}, nil
}

func (r *Router) publicModelName(model string) string {
	model = strings.TrimSpace(model)
	if r != nil && r.models != nil {
		if public := r.models.publicModelName(model); public != "" {
			return public
		}
	}
	return model
}

func (r *Router) owned(model string) bool {
	if hasCommandCodePrefix(model) {
		return true
	}
	if r.models != nil && r.models.owns(model) {
		return true
	}
	if r.cfg == nil {
		return false
	}
	if _, ok := r.cfg.modelSet()[normalizeModel(model)]; ok {
		return true
	}
	return false
}

func hasCommandCodePrefix(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), Provider+"/")
}

func stripCommandCodePrefix(model string) string {
	model = strings.TrimSpace(model)
	if !hasCommandCodePrefix(model) {
		return model
	}
	return strings.TrimSpace(model[len(Provider)+1:])
}

func hasProvider(providers []string, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	if wanted == "" {
		return false
	}
	for _, provider := range providers {
		if strings.ToLower(strings.TrimSpace(provider)) == wanted {
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
