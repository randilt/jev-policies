package jevmodelrouter

import (
	"context"
	"os"
	"testing"

	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

// TestRoute_SetsUpstreamNameAndMetadata is a fast, no-network unit test of
// the actual routing mechanism — this is the part that was wrong before
// (writing to DynamicMetadata instead of UpstreamName + reqCtx.Metadata),
// and is worth covering independent of the live-Jev integration test below.
func TestRoute_SetsUpstreamNameAndMetadata(t *testing.T) {
	p := &RouterPolicy{}
	reqCtx := &policy.RequestContext{SharedContext: &policy.SharedContext{}}

	action := p.route(reqCtx, "gpt-4o")

	mods, ok := action.(policy.UpstreamRequestModifications)
	if !ok {
		t.Fatalf("expected UpstreamRequestModifications, got %T", action)
	}
	if mods.UpstreamName == nil || *mods.UpstreamName != "gpt-4o" {
		t.Fatalf("unexpected UpstreamName: %v", mods.UpstreamName)
	}
	if reqCtx.Metadata["selected_provider"] != "gpt-4o" {
		t.Fatalf("unexpected Metadata[selected_provider]: %v", reqCtx.Metadata["selected_provider"])
	}
}

// TestRoute_EmptyProviderClearsStaleMetadata matches
// cost-based-model-routing's applyProviderRouting behavior: an empty
// providerID (unset fallbackProvider on a failure path) must not leave a
// stale selected_provider key from an earlier policy in the chain.
func TestRoute_EmptyProviderClearsStaleMetadata(t *testing.T) {
	p := &RouterPolicy{}
	reqCtx := &policy.RequestContext{
		SharedContext: &policy.SharedContext{
			Metadata: map[string]interface{}{"selected_provider": "stale-value"},
		},
	}

	action := p.route(reqCtx, "")

	mods, ok := action.(policy.UpstreamRequestModifications)
	if !ok {
		t.Fatalf("expected UpstreamRequestModifications, got %T", action)
	}
	if mods.UpstreamName != nil {
		t.Fatalf("expected no UpstreamName for empty providerID, got %v", *mods.UpstreamName)
	}
	if _, exists := reqCtx.Metadata["selected_provider"]; exists {
		t.Fatalf("expected stale selected_provider metadata to be cleared, still present: %v", reqCtx.Metadata["selected_provider"])
	}
}

// TestRoute_DefaultCandidateResolvesToEmptyProvider matches the real bug
// found testing against a live gateway: an LlmProxy's primary provider has
// no named UpstreamDefinition cluster, so routing Jev's choice straight
// through as UpstreamName produces an Envoy "cluster_not_found" for it.
// defaultCandidate must make route() treat that choice exactly like an
// empty providerID.
func TestRoute_DefaultCandidateResolvesToEmptyProvider(t *testing.T) {
	p := &RouterPolicy{defaultCandidate: "gpt-4o-mini-azure-open-ai"}
	reqCtx := &policy.RequestContext{
		SharedContext: &policy.SharedContext{
			Metadata: map[string]interface{}{"selected_provider": "stale-value"},
		},
	}

	action := p.route(reqCtx, "gpt-4o-mini-azure-open-ai")

	mods, ok := action.(policy.UpstreamRequestModifications)
	if !ok {
		t.Fatalf("expected UpstreamRequestModifications, got %T", action)
	}
	if mods.UpstreamName != nil {
		t.Fatalf("expected no UpstreamName when routing to defaultCandidate, got %v", *mods.UpstreamName)
	}
	if _, exists := reqCtx.Metadata["selected_provider"]; exists {
		t.Fatalf("expected selected_provider metadata to be cleared when routing to defaultCandidate, still present: %v", reqCtx.Metadata["selected_provider"])
	}
}

// TestGetPolicy_RejectsUnknownDefaultCandidate ensures a misconfigured
// defaultCandidate (a typo, or one that doesn't match any candidates key)
// fails fast at policy construction instead of silently never matching.
func TestGetPolicy_RejectsUnknownDefaultCandidate(t *testing.T) {
	_, err := GetPolicy(policy.PolicyMetadata{}, map[string]interface{}{
		"apiKey": "test-key",
		"candidates": map[string]interface{}{
			"gpt-4o-mini": "cheap",
		},
		"defaultCandidate": "not-a-candidate",
	})
	if err == nil {
		t.Fatal("expected GetPolicy to reject a defaultCandidate not present in candidates")
	}
}

// TestJevModelRouter_OnRequestBody exercises the policy directly (no Envoy,
// no gateway runtime) against the real Jev API, using TYPESAFE_API_KEY from
// the environment. Skips if the key isn't set.
func TestJevModelRouter_OnRequestBody(t *testing.T) {
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		t.Skip("TYPESAFE_API_KEY not set; skipping live Jev API test")
	}

	candidates := map[string]interface{}{
		"gpt-4o-mini": "Cheap, fast model. Best for short, casual, simple requests that don't need deep reasoning.",
		"gpt-4o":      "Powerful reasoning model. Best for complex multi-step analysis, coding, and detailed technical explanations.",
	}

	p, err := GetPolicy(policy.PolicyMetadata{}, map[string]interface{}{
		"apiKey":     apiKey,
		"candidates": candidates,
	})
	if err != nil {
		t.Fatalf("GetPolicy failed: %v", err)
	}
	router, ok := p.(*RouterPolicy)
	if !ok {
		t.Fatalf("GetPolicy returned unexpected type %T", p)
	}

	cases := []struct {
		name         string
		body         string
		wantProvider string
	}{
		{
			name:         "casual message routes to the cheap model",
			body:         `{"message": "hey what's up, how's it going?"}`,
			wantProvider: "gpt-4o-mini",
		},
		{
			name: "complex reasoning request routes to the powerful model",
			body: `{"message": "Write a rigorous proof that there are infinitely many prime numbers, ` +
				`then analyze the time complexity of the Sieve of Eratosthenes and explain where it ` +
				`could be optimized for very large bounds."}`,
			wantProvider: "gpt-4o",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx := &policy.RequestContext{
				SharedContext: &policy.SharedContext{},
				Body: &policy.Body{
					Content:     []byte(tc.body),
					Present:     true,
					EndOfStream: true,
				},
			}

			action := router.OnRequestBody(context.Background(), reqCtx, nil)

			mods, ok := action.(policy.UpstreamRequestModifications)
			if !ok {
				t.Fatalf("expected UpstreamRequestModifications (a routing decision), got %T (action was nil or unrouted)", action)
			}
			if mods.UpstreamName == nil {
				t.Fatalf("expected UpstreamName to be set, got nil")
			}
			t.Logf("routed to (UpstreamName): %v", *mods.UpstreamName)
			t.Logf("routed to (Metadata): %v", reqCtx.Metadata["selected_provider"])

			if *mods.UpstreamName != tc.wantProvider {
				t.Errorf("body %q: UpstreamName = %v, want %q", tc.body, *mods.UpstreamName, tc.wantProvider)
			}
			if reqCtx.Metadata["selected_provider"] != tc.wantProvider {
				t.Errorf("body %q: Metadata[selected_provider] = %v, want %q", tc.body, reqCtx.Metadata["selected_provider"], tc.wantProvider)
			}
		})
	}
}
