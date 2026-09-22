# jev-guardrail

A guardrail policy for [WSO2 API Platform](https://github.com/wso2/api-platform)'s AI Gateway, backed by [TypeSafe AI's Jev](https://typesafe.ai/) model. Screens a request body for jailbreak attempts, harmful requests, and self-harm signals, and blocks it (HTTP 422) when Jev's confidence crosses a configurable threshold.

**Status: proof of concept.** Built to answer one question — does Jev fit the API Platform's policy SDK contract and actually work — not to be production-ready as-is. See [Limitations](#limitations).

## How it works

Implements `policyv1alpha2.RequestPolicy` from [`sdk/core/policy/v1alpha2`](https://github.com/wso2/api-platform/blob/main/sdk/core/policy/v1alpha2/interface.go). On each request it sends the body to Jev's `POST /v1/systemone` endpoint with three typed questions — `jailbreak`, `harmful_request` (both `Noul`, i.e. calibrated yes/no probabilities), and `severity` (a `Score`) — ported from TypeSafe's own [`llm_guardrails` cookbook](https://docs.typesafe.ai/cookbooks/llm_guardrails). If any answer crosses its threshold, it returns an `ImmediateResponse` with the same `type`/`message`/`assessments` envelope shape the platform's built-in guardrails (e.g. `regex-guardrail`) already use.

## Parameters

See [`policy-definition.yaml`](./policy-definition.yaml). Key ones: `apiKey` (required, TypeSafe API key), `noulThreshold` (default `0.7`), `severityThreshold` (default `2.0`), `onError` (`failOpen` / `failClosed` — required choice, since Jev has no self-hosted fallback if it's unreachable).

## Test results

`jevguardrail_test.go` calls the policy directly against the live Jev API (no gateway/Envoy involved) with 3 prompts:

| Prompt | Result |
|---|---|
| Benign question | passed through |
| Jailbreak attempt | blocked — `jailbreak` probability `0.99` (threshold `0.7`) |
| Harmful-content request | blocked — `harmful_request` `0.98`, `severity` `3` (threshold `2`) |

Run it yourself:
```bash
echo "TYPESAFE_API_KEY=<your key>" > .env
set -a && source .env && set +a && go test ./... -v
```

3 hand-picked prompts prove the integration is mechanically sound — the SDK contract, the Jev API, and the block envelope all fit together correctly. It says nothing about false-positive/negative rates on ambiguous input or behavior under production load.

## Limitations

- Screens the entire raw request body as-is, instead of extracting the actual prompt text from a provider-specific JSON shape (a real implementation would add a `jsonPath` param, like `regex-guardrail` does).
- Request phase only — no response-phase check yet.
- No streaming support.
- Jev is hosted-only SaaS with no self-host/VPC option (as of writing) — every request body sent through this policy leaves your environment to TypeSafe's API.
