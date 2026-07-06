package findings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseResponse extracts findings from a model response. It tolerates
// accidental code fences and surrounding prose by locating the outermost
// JSON object, then strict-decodes the {"findings": [...]} contract
// (unknown fields are an error — the corrective retry handles it).
func ParseResponse(text string) ([]Finding, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in response")
	}
	dec := json.NewDecoder(strings.NewReader(text[start : end+1]))
	dec.DisallowUnknownFields()
	var out struct {
		Findings []Finding `json:"findings"`
	}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("response does not match findings contract: %w", err)
	}
	return out.Findings, nil
}
