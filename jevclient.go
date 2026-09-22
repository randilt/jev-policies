package jevguardrail

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

// noulQuestion is a calibrated yes/no question.
type noulQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// scoreQuestion is an ordinal question over an operator-defined scale.
type scoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

type systemOneRequest struct {
	State     string      `json:"state"`
	Model     string      `json:"model"`
	Questions interface{} `json:"questions"`
}

type nounAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"`
}

type scoreAnswer struct {
	Type       string  `json:"type"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

// systemOneResponse only decodes the subset of the response this guardrail
// needs. Noul answers carry a probability directly; score answers carry a
// score plus a confidence. Decoding into json.RawMessage per-answer keeps
// this client agnostic to which question types the caller asked.
type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
}

// systemOne calls POST /v1/systemone with the given state and questions and
// returns the raw per-question answers for the caller to interpret.
func (c *jevClient) systemOne(ctx context.Context, state string, questions map[string]interface{}) (map[string]json.RawMessage, error) {
	reqBody := systemOneRequest{
		State:     state,
		Model:     c.model,
		Questions: questions,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}

	url := c.baseURL + "/v1/systemone"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed systemOneResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}
	return parsed.Answers, nil
}

// noulProbability decodes a Noul answer's probability from a raw answer.
func noulProbability(raw json.RawMessage) (float64, error) {
	var a nounAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return 0, fmt.Errorf("jev: decode noul answer: %w", err)
	}
	return a.Noul, nil
}

// scoreValue decodes a Score answer's position on the scale from a raw answer.
func scoreValue(raw json.RawMessage) (float64, error) {
	var a scoreAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return 0, fmt.Errorf("jev: decode score answer: %w", err)
	}
	return a.Score, nil
}
