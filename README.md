# jev-policies

A collection of [WSO2 API Platform](https://github.com/wso2/api-platform) AI Gateway policies backed by [TypeSafe AI's Jev](https://typesafe.ai/) model — a "System One" model that answers typed questions (`Noul` = calibrated yes/no, `Choice` = categorical, `Score` = ordinal) about a piece of state, rather than generating text.

Each policy is its own Go module in its own directory, versioned and buildable independently — mirroring how the platform's own vendor guardrails (`aws-bedrock-guardrail`, `azure-content-safety-content-moderation`, etc.) are organized in [`wso2/gateway-controllers`](https://github.com/wso2/gateway-controllers). None of these are vendored into `api-platform` core; each is opted into a gateway build via a `gomodule:`/`filePath:` entry in `build.yaml`.

**Status: proof of concept, all of them.** See each policy's own README for what's been validated and what's still missing before it'd be production-ready.

## Policies

| Policy | Description |
|---|---|
| [`guardrail/`](./guardrail) | Screens request/response bodies for jailbreak, harmful-content, and self-harm signals; blocks via the platform's standard guardrail envelope. |
| [`model-router/`](./model-router) | Routes each request to the best-fit LLM provider from a configured candidate set, via the platform's existing `selected_provider` metadata mechanism. |

## Common constraint

Jev is closed, hosted-only SaaS — there's no self-host/VPC option as of writing, so every one of these policies sends request (and sometimes response) data to TypeSafe's API. Every policy here requires an explicit `apiKey` param and is opt-in only, never a default.

## Using one of these

```yaml
# build.yaml
policies:
  - name: jev-guardrail
    gomodule: github.com/randilt/jev-policies/guardrail@guardrail/v0.1.0
  - name: jev-model-router
    gomodule: github.com/randilt/jev-policies/model-router@model-router/v0.1.0
```

Go's subdirectory-module versioning means each policy's tag is prefixed with its own directory name (`guardrail/vX.Y.Z`, `model-router/vX.Y.Z`), even though they live in one repo.
