package jevmodelrouter

import (
	"context"
	"os"
	"testing"

	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

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
			got := mods.DynamicMetadata[extProcMetadataNamespace]["selected_provider"]
			t.Logf("routed to: %v", got)

			if got != tc.wantProvider {
				t.Errorf("body %q: routed to %v, want %q", tc.body, got, tc.wantProvider)
			}
		})
	}
}
