package jevmodelrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api.typesafe.ai"

// jevClient is a minimal client for TypeSafe's Jev "System One" API.
// There is no official Go SDK; the REST contract (POST /v1/systemone) is
// simple enough to call directly. See https://docs.typesafe.ai/api.
type jevClient struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

func newJevClient(apiKey, baseURL, model string) *jevClient {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if model == "" {
		model = "jev-latest"
	}
	return &jevClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// choiceQuestion asks Jev to pick one of a fixed set of labeled candidates.
type choiceQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type systemOneRequest struct {
	State     string                    `json:"state"`
	Model     string                    `json:"model"`
	Questions map[string]choiceQuestion `json:"questions"`
}

type choiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type systemOneResponse struct {
	Model   string                  `json:"model"`
	Answers map[string]choiceAnswer `json:"answers"`
}

// pickProvider calls POST /v1/systemone with a single "provider" Choice
// question over candidates, and returns Jev's pick plus its confidence.
func (c *jevClient) pickProvider(ctx context.Context, state string, candidates map[string]string) (choice string, confidence float64, err error) {
	reqBody := systemOneRequest{
		State: state,
		Model: c.model,
		Questions: map[string]choiceQuestion{
			"provider": {
				Type:         "choice",
				Instructions: "Which provider is the best fit to serve this request?",
				Criteria:     candidates,
			},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("jev: encode request: %w", err)
	}

	url := c.baseURL + "/v1/systemone"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("jev: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("jev: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("jev: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed systemOneResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("jev: decode response: %w", err)
	}

	answer, ok := parsed.Answers["provider"]
	if !ok {
		return "", 0, fmt.Errorf("jev: response missing 'provider' answer")
	}
	return answer.Choice, answer.Confidence, nil
}
