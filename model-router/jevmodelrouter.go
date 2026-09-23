// Package jevmodelrouter is an LLM provider/model routing policy backed by
// TypeSafe AI's Jev "System One" model (https://typesafe.ai). It asks Jev to
// pick the best-fit candidate provider for each request and routes to it the
// same way every built-in routing policy in gateway-controllers does
// (intelligent-model-routing, semantic-model-routing, time-based-model-routing,
// cost-based-model-routing all agree on this exact pattern):
//   - set UpstreamRequestModifications.UpstreamName to the chosen provider's
//     alias, which points the request at that named upstream directly, and
//   - write reqCtx.Metadata["selected_provider"] = <alias> so downstream
//     per-provider policies (auth, transformers) that key off that value in
//     the same request see the same selection.
//
// An earlier version of this policy set the selection via
// UpstreamRequestModifications.DynamicMetadata instead, reasoning from the
// ext_proc kernel's dynamic-metadata plumbing rather than from how the
// platform's own routing policies actually do it. That version's Jev call
// and decision logic worked (confirmed via its own debug logs), but the
// upstream was never actually repointed, because DynamicMetadata isn't the
// mechanism any shipped router uses. Fixed here.
//
// A second, separate bug fixed here: candidates aren't necessarily
// additionalProviders. An LlmProxy's primary/default provider is never
// registered as a named UpstreamDefinition (only additionalProviders are —
// see gateway-controller/pkg/utils/llm_transformer.go's additionalProviders
// loop), so setting UpstreamName to the primary provider's own id/alias
// points at a cluster that was never created (Envoy: "cluster_not_found").
// Every built-in routing policy (e.g. cost-based-model-routing's `target`
// struct) handles this with an explicit convention: an empty provider
// string means "use the LLM proxy's primary/default provider", i.e. don't
// set UpstreamName at all. This policy adopts the same convention via the
// optional defaultCandidate param: when Jev's choice equals defaultCandidate,
// route() is called with "" instead of the candidate's own id.
//
// Known gaps deliberately left for a future iteration:
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

// metadataProviderRouting is the SharedContext.Metadata key the platform's
// existing provider-selection mechanism reads
// (gateway-controller/pkg/utils/llm_transformer.go: selectedProviderExecutionCondition
// gates each configured additionalProviders[] entry on this key). Matches
// the constant of the same name/value in every built-in routing policy.
const metadataProviderRouting = "selected_provider"

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
	defaultCandidate    string
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

	// defaultCandidate names the candidate that represents the LLM proxy's
	// own primary/default provider (as opposed to one of its
	// additionalProviders). Picking it must not set UpstreamName — see the
	// package doc comment.
	defaultCandidate, _ := params["defaultCandidate"].(string)
	if defaultCandidate != "" {
		if _, known := candidates[defaultCandidate]; !known {
			return nil, fmt.Errorf("jev-model-router: defaultCandidate %q must be one of the configured candidates", defaultCandidate)
		}
	}

	return &RouterPolicy{
		client:              newJevClient(apiKey, baseURL, model),
		candidates:          candidates,
		confidenceThreshold: confidenceThreshold,
		fallbackProvider:    fallbackProvider,
		defaultCandidate:    defaultCandidate,
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
		return p.route(reqCtx, p.fallbackProvider)
	}

	if _, known := p.candidates[choice]; !known {
		slog.Warn("[jev-model-router] Jev picked an unconfigured candidate, falling back", "choice", choice)
		return p.route(reqCtx, p.fallbackProvider)
	}
	if confidence < p.confidenceThreshold {
		slog.Info("[jev-model-router] confidence below threshold, falling back",
			"choice", choice, "confidence", confidence, "threshold", p.confidenceThreshold)
		return p.route(reqCtx, p.fallbackProvider)
	}

	slog.Info("[jev-model-router] routed", "choice", choice, "confidence", confidence)
	return p.route(reqCtx, choice)
}

// route points the request at providerID's named upstream and publishes the
// engine's selected_provider contract key, exactly as every built-in routing
// policy's applyProviderRouting-equivalent does. providerID is resolved to ""
// (meaning "use the LLM proxy's primary/default provider", so no upstream
// name is set) when it's empty already (an unset fallbackProvider on a
// failure path) or when it equals defaultCandidate (Jev explicitly chose the
// primary provider, which has no named upstream to route to — see the
// package doc comment). Either way, any selected_provider left behind by an
// earlier policy in the chain is removed — stale routing metadata would
// otherwise select the wrong provider.
func (p *RouterPolicy) route(reqCtx *policy.RequestContext, providerID string) policy.RequestAction {
	if reqCtx.Metadata == nil {
		reqCtx.Metadata = make(map[string]interface{})
	}
	if providerID != "" && p.defaultCandidate != "" && providerID == p.defaultCandidate {
		providerID = ""
	}
	if providerID == "" {
		delete(reqCtx.Metadata, metadataProviderRouting)
		return policy.UpstreamRequestModifications{}
	}
	reqCtx.Metadata[metadataProviderRouting] = providerID
	return policy.UpstreamRequestModifications{
		UpstreamName: &providerID,
	}
}
