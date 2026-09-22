# jev-model-router

An LLM provider/model routing policy for [WSO2 API Platform](https://github.com/wso2/api-platform)'s AI Gateway, backed by [TypeSafe AI's Jev](https://typesafe.ai/) model. Asks Jev to pick the best-fit provider from a configured candidate set for each request, and routes to it.

**Status: proof of concept.** See [Limitations](#limitations).

## How it works

Implements `policyv1alpha2.RequestPolicy`. Routing in this platform is already just ordinary policies writing a `selected_provider` value into dynamic metadata — the xDS-generated route config gates each configured `additionalProviders[].id` on that key (`selectedProviderExecutionCondition` in `gateway-controller/pkg/utils/llm_transformer.go`), regardless of which policy set it. This policy plugs into that exact mechanism: it sends the request body to Jev's `POST /v1/systemone` as a single `Choice` question over the configured candidates, and on a confident answer, sets

```go
DynamicMetadata: map[string]map[string]any{
    "api_platform.policy_engine.envoy.filters.http.ext_proc": {
        "selected_provider": choice,
    },
}
```

No gateway-controller changes needed — same approach the built-in `llm-header-router`/`model-round-robin` policies use.

## Parameters

See [`policy-definition.yaml`](./policy-definition.yaml). Key ones: `apiKey` (required), `candidates` (required — map of `providerId -> description`, where each id must match an `additionalProviders[].id` on the `LlmProxy`), `confidenceThreshold` (default `0.5`), `fallbackProvider` (optional — candidate to use on any failure; left empty, the request just falls through to the `LlmProxy`'s own default provider).

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

Two prompts prove the SDK contract, the Jev API, and the `selected_provider` metadata mechanism all fit together — it says nothing about routing quality on ambiguous prompts, candidate sets larger than two, or behavior under production load.

## Limitations

- Sends the whole raw request body as Jev's `state`, instead of a minimized summary (recent message roles/truncated text, tool/vision signals) the way the reference [`prismhq/jev-router`](https://github.com/prismhq/jev-router) project does.
- No eligibility/capability filtering of candidates (vision support, tool support, context length) before asking Jev to choose among them.
- No streaming support.
- Jev is hosted-only SaaS with no self-host/VPC option (as of writing) — every request body sent through this policy leaves your environment to TypeSafe's API.
