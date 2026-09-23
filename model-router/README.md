# jev-model-router

An LLM provider/model routing policy for [WSO2 API Platform](https://github.com/wso2/api-platform)'s AI Gateway, backed by [TypeSafe AI's Jev](https://typesafe.ai/) model. Asks Jev to pick the best-fit provider from a configured candidate set for each request, and routes to it.

**Status: proof of concept, routing mechanism verified end-to-end on a real running gateway.** See [Limitations](#limitations).

## How it works

Implements `policyv1alpha2.RequestPolicy`. Routing in this platform is already just ordinary policies writing a `selected_provider` value into request metadata — the xDS-generated route config gates each configured `additionalProviders[].id` on that key (`selectedProviderExecutionCondition` in `gateway-controller/pkg/utils/llm_transformer.go`), regardless of which policy set it. This policy plugs into that exact mechanism: it sends the request body to Jev's `POST /v1/systemone` as a single `Choice` question over the configured candidates, and on a confident answer, sets

```go
reqCtx.Metadata["selected_provider"] = choice
return policy.UpstreamRequestModifications{
    UpstreamName: &choice,
}
```

Confirmed against **four** built-in routing policies in [`wso2/gateway-controllers`](https://github.com/wso2/gateway-controllers) (`intelligent-model-routing`, `semantic-model-routing`, `time-based-model-routing`, `cost-based-model-routing`) — all four agree on exactly this pattern: set `UpstreamName` directly, and write `reqCtx.Metadata["selected_provider"]` in place. An earlier version of this policy set the selection via `UpstreamRequestModifications.DynamicMetadata` instead (reasoning from the ext_proc kernel's dynamic-metadata plumbing rather than from how the shipped routers actually do it) — that version's Jev call and decision logic worked correctly, but the upstream was never actually repointed, because `DynamicMetadata` isn't the mechanism any built-in router uses. No gateway-controller changes needed either way — this is purely an in-policy fix.

**A second bug, found only once this was tested against a real running gateway (not just direct Go-level calls):** an `LlmProxy`'s primary/default provider is never registered as a named `UpstreamDefinition` cluster — only `additionalProviders` are (`gateway-controller/pkg/utils/llm_transformer.go`'s `additionalProviders` loop). Every built-in router handles this with an explicit convention (see `cost-based-model-routing`'s `target` struct: "An empty Provider means the LLM proxy's primary/default provider"). This policy had no equivalent, so a `candidates` config that includes the primary provider's own id would set `UpstreamName` to a cluster that was never created — Envoy responds `503 cluster_not_found`, even though Jev's decision logic and confidence were completely correct. Fixed via the new `defaultCandidate` parameter: when Jev picks that candidate, `route()` treats it exactly like an empty providerID (no `UpstreamName` set, falls through to the proxy's actual default).

## Parameters

See [`policy-definition.yaml`](./policy-definition.yaml). Key ones: `apiKey` (required), `candidates` (required — map of `providerId -> description`; normally each id matches an `additionalProviders[].id` on the `LlmProxy`, but the primary provider can also be included as a candidate — see `defaultCandidate` below), `confidenceThreshold` (default `0.5`), `fallbackProvider` (optional — candidate to use on any failure; left empty, the request just falls through to the `LlmProxy`'s own default provider), `defaultCandidate` (optional — the `candidates` key, if any, that represents the primary provider itself; required only when the primary provider is one of the candidates).

## Test results

`jevmodelrouter_test.go` calls the policy directly against the live Jev API (no gateway/Envoy involved) with two candidates — a cheap/fast model and a powerful reasoning model — and two prompts:

| Prompt | Routed to | Confidence |
|---|---|---|
| "hey what's up, how's it going?" | `gpt-4o-mini` | 1.0 |
| Multi-step proof + complexity analysis request | `gpt-4o` | 1.0 |

Run it yourself:
```bash
echo "TYPESAFE_API_KEY=<your key>" > .env
set -a && source .env && set +a && go test ./... -v
```

Two prompts prove the SDK contract, the Jev API, and the routing mechanism all fit together — it says nothing about routing quality on ambiguous prompts, candidate sets larger than two, or behavior under production load. `TestRoute_SetsUpstreamNameAndMetadata`, `TestRoute_EmptyProviderClearsStaleMetadata`, `TestRoute_DefaultCandidateResolvesToEmptyProvider`, and `TestGetPolicy_RejectsUnknownDefaultCandidate` cover the routing mechanism itself (no network needed) — including the stale-metadata cleanup on the empty-`fallbackProvider` path and the `defaultCandidate` primary-provider fix, both matching `cost-based-model-routing`'s `applyProviderRouting` behavior.

**End-to-end verified** on a real self-hosted gateway (`wso2apip-ai-gateway`), routed through an `azure-openai-provider-proxy` LlmProxy with a primary provider (`gpt-4o-mini-azure-open-ai`) and one `additionalProviders` entry (`fifififi`). This is what surfaced the `defaultCandidate` bug above — direct Go-level calls to `OnRequestBody` had confirmed Jev's decision logic (`v0.1.1`), but the real Envoy routing still 503'd (`cluster_not_found`) until `defaultCandidate` was added (`v0.1.2`), because the live route's `candidates` config included the primary provider by its own id.

## Limitations

- Sends the whole raw request body as Jev's `state`, instead of a minimized summary (recent message roles/truncated text, tool/vision signals) the way the reference [`prismhq/jev-router`](https://github.com/prismhq/jev-router) project does.
- No eligibility/capability filtering of candidates (vision support, tool support, context length) before asking Jev to choose among them.
- No streaming support.
- Jev is hosted-only SaaS with no self-host/VPC option (as of writing) — every request body sent through this policy leaves your environment to TypeSafe's API.
