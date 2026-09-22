// Package jevmodelrouter is a proof-of-concept LLM provider/model routing
// policy backed by TypeSafe AI's Jev "System One" model (https://typesafe.ai).
// It asks Jev to pick the best-fit candidate provider for each request and
// writes that choice into the dynamic metadata key the platform's existing
// provider-selection mechanism already reads
// (gateway-controller/pkg/utils/llm_transformer.go: selectedProviderExecutionCondition,
// which gates each configured additionalProviders[] entry on
// request.Metadata['selected_provider']). No gateway-controller changes are
// needed — this policy only has to set that one key, the same way the
// built-in llm-header-router/model-round-robin policies do.
//
// This is a dev-policy PoC, not a production implementation. Known gaps
// deliberately left for a real implementation:
//   - Sends the whole raw request body as the Jev `state`, rather than a
//     minimized summary (recent message roles/truncated text, tool/vision
//     signals) the way the reference prismhq/jev-router project does.
//   - No eligibility/capability filtering of candidates (vision, tool
//     support, context length) before asking Jev to choose among them.
//   - No streaming support.
package jevmodelrouter

import (
	"context"
	"fmt"
	"log/slog"

	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

// extProcMetadataNamespace is the dynamic-metadata namespace the platform's
// ext_proc kernel merges a policy's UpstreamRequestModifications.DynamicMetadata
// into, and the same namespace the generated CEL routing condition reads as
// request.Metadata. Confirmed against gateway-runtime/policy-engine/internal/constants
// and gateway-controller/pkg/constants, which both define this exact string.
const extProcMetadataNamespace = "api_platform.policy_engine.envoy.filters.http.ext_proc"

// maxStateChars bounds how much of the raw request body is sent to Jev as
// state, so an oversized payload doesn't blow up latency/cost on a
// per-request routing decision.
const maxStateChars = 4000

// RouterPolicy asks Jev to pick the best-fit provider for each request from
// a configured candidate set, and routes to it via the existing
// selected_provider metadata mechanism.
//
// Unlike the guardrail policy, there's no fail-open/fail-closed choice here
// — a routing decision has no "block" semantics. On any failure to get a
// confident, valid choice from Jev, it falls back to fallbackProvider if
// configured, or otherwise leaves selected_provider unset so the gateway's
// own default-provider handling applies (see route()).
type RouterPolicy struct {
	client              *jevClient
	candidates          map[string]string
	confidenceThreshold float64
	fallbackProvider    string
}

// GetPolicy is the factory function the gateway (or, here, a standalone
// caller) uses to construct the policy instance from its configured params.
func GetPolicy(metadata policy.PolicyMetadata, params map[string]interface{}) (policy.Policy, error) {
	apiKey, _ := params["apiKey"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("jev-model-router: apiKey is required")
	}
	baseURL, _ := params["baseURL"].(string)
	model, _ := params["model"].(string)

	candidatesRaw, ok := params["candidates"].(map[string]interface{})
	if !ok || len(candidatesRaw) == 0 {
		return nil, fmt.Errorf("jev-model-router: candidates is required and must be a non-empty map of providerId -> description")
	}
	candidates := make(map[string]string, len(candidatesRaw))
	for id, desc := range candidatesRaw {
		descStr, ok := desc.(string)
		if !ok {
			return nil, fmt.Errorf("jev-model-router: candidates[%q] must be a string description", id)
		}
		candidates[id] = descStr
	}

	confidenceThreshold := 0.5
	if v, ok := params["confidenceThreshold"].(float64); ok {
		confidenceThreshold = v
	}
	fallbackProvider, _ := params["fallbackProvider"].(string)

	return &RouterPolicy{
		client:              newJevClient(apiKey, baseURL, model),
		candidates:          candidates,
		confidenceThreshold: confidenceThreshold,
		fallbackProvider:    fallbackProvider,
	}, nil
}

// Mode declares this policy as request-body-only.
func (p *RouterPolicy) Mode() policy.ProcessingMode {
	return policy.ProcessingMode{
		RequestHeaderMode:  policy.HeaderModeSkip,
		RequestBodyMode:    policy.BodyModeBuffer,
		ResponseHeaderMode: policy.HeaderModeSkip,
		ResponseBodyMode:   policy.BodyModeSkip,
	}
}

// OnRequestBody asks Jev to pick a provider and routes to it via
// selected_provider dynamic metadata. Returning an action with no metadata
// set (nil, or a fallbackProvider-less failure) leaves the request to the
// gateway's own default-provider handling, per selectedProviderExecutionCondition's
// includeDefault branch.
func (p *RouterPolicy) OnRequestBody(ctx context.Context, reqCtx *policy.RequestContext, params map[string]interface{}) policy.RequestAction {
	if reqCtx.Body == nil || !reqCtx.Body.Present || len(reqCtx.Body.Content) == 0 {
		return nil
	}

	state := string(reqCtx.Body.Content)
	if len(state) > maxStateChars {
		state = state[:maxStateChars]
	}

	choice, confidence, err := p.client.pickProvider(ctx, state, p.candidates)
	if err != nil {
		slog.Error("[jev-model-router] Jev call failed", "error", err)
		return p.route(p.fallbackProvider)
	}

	if _, known := p.candidates[choice]; !known {
		slog.Warn("[jev-model-router] Jev picked an unconfigured candidate, falling back", "choice", choice)
		return p.route(p.fallbackProvider)
	}
	if confidence < p.confidenceThreshold {
		slog.Info("[jev-model-router] confidence below threshold, falling back",
			"choice", choice, "confidence", confidence, "threshold", p.confidenceThreshold)
		return p.route(p.fallbackProvider)
	}

	slog.Info("[jev-model-router] routed", "choice", choice, "confidence", confidence)
	return p.route(choice)
}

// route sets selected_provider when providerID is non-empty, or passes the
// request through untouched (leaving it to the gateway's default provider)
// when it's empty — the empty case covers an unset fallbackProvider on any
// failure path.
func (p *RouterPolicy) route(providerID string) policy.RequestAction {
	if providerID == "" {
		return nil
	}
	return policy.UpstreamRequestModifications{
		DynamicMetadata: map[string]map[string]any{
			extProcMetadataNamespace: {
				"selected_provider": providerID,
			},
		},
	}
}
