package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var defaultOpenAICompatibleModelIDs = []string{
	"gpt-5-codex",
	"gpt-5",
	"gpt-5-mini",
	"gpt-5-nano",
	"gpt-4.1",
	"gpt-4.1-mini",
	"gpt-4.1-nano",
	"gpt-4o",
	"gpt-4o-mini",
	"o4-mini",
	"o3",
	"o3-mini",
}

func isOpenAICompatiblePoolKeyAdmission(admission AdmissionResult) bool {
	return admission.Kind == AdmissionKindPoolUser && admission.CredentialKind == CredentialKindOpenAICompatiblePoolKey
}

func isOpenAICompatibleClientTraffic(admission AdmissionResult, path string) bool {
	return isOpenAICompatiblePoolKeyAdmission(admission) && isOpenAICompatibleEndpointPath(path)
}

func isOpenAICompatibleEndpointPath(path string) bool {
	path = strings.TrimSpace(path)
	switch path {
	case "/v1/responses", "/v1/chat/completions", "/v1/models":
		return true
	}
	return strings.HasPrefix(path, "/v1/responses/")
}

func isOpenAICompatibleModelRequestPath(path string) bool {
	path = strings.TrimSpace(path)
	return path == "/v1/responses" || path == "/v1/chat/completions" || strings.HasPrefix(path, "/v1/responses/")
}

func poolAPITokenAllowsModel(allowedModels []string, requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return true
	}
	patterns := cleanModelAllowlist(allowedModels)
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == "*" {
			return true
		}
		if strings.EqualFold(pattern, requestedModel) || wildcardModelMatch(pattern, requestedModel) {
			return true
		}
	}
	return false
}

func cleanModelAllowlist(allowedModels []string) []string {
	if len(allowedModels) == 0 {
		return nil
	}
	out := make([]string, 0, len(allowedModels))
	seen := make(map[string]struct{}, len(allowedModels))
	for _, model := range allowedModels {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

func wildcardModelMatch(pattern, model string) bool {
	if !strings.Contains(pattern, "*") {
		return false
	}
	pattern = strings.ToLower(pattern)
	model = strings.ToLower(model)
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 0 {
		return true
	}
	pos := 0
	if first := parts[0]; first != "" {
		if !strings.HasPrefix(model, first) {
			return false
		}
		pos = len(first)
	}
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		idx := strings.Index(model[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	if last := parts[len(parts)-1]; last != "" && !strings.HasSuffix(model, last) {
		return false
	}
	return true
}

func validateOpenAICompatibleModelAllowlist(routePlan RoutePlan) error {
	if !routePlan.IsOpenAICompatibleClient || !isOpenAICompatibleModelRequestPath(routePlan.Shape.Path) {
		return nil
	}
	model := strings.TrimSpace(routePlan.Shape.RequestedModel)
	if poolAPITokenAllowsModel(routePlan.Admission.TokenAllowedModels, model) {
		return nil
	}
	return fmt.Errorf("model %q is not allowed for this pool API token", model)
}

func writeOpenAICompatibleModelAllowlistError(w http.ResponseWriter, err error) {
	if err == nil || w == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "invalid_request_error",
			"code":    "model_not_allowed",
		},
	})
}

func openAICompatibleModelIDsForAllowlist(allowedModels []string) []string {
	patterns := cleanModelAllowlist(allowedModels)
	if len(patterns) == 0 || allowlistContainsGlobalWildcard(patterns) {
		return append([]string(nil), defaultOpenAICompatibleModelIDs...)
	}
	out := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns)+len(defaultOpenAICompatibleModelIDs))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	for _, pattern := range patterns {
		if strings.Contains(pattern, "*") {
			for _, id := range defaultOpenAICompatibleModelIDs {
				if wildcardModelMatch(pattern, id) {
					add(id)
				}
			}
			continue
		}
		add(pattern)
	}
	return out
}

func allowlistContainsGlobalWildcard(patterns []string) bool {
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "*" {
			return true
		}
	}
	return false
}

func buildOpenAICompatibleModelsEntry(allowedModels []string) codexModelsCacheEntry {
	ids := openAICompatibleModelIDsForAllowlist(allowedModels)
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  int64(0),
			"owned_by": "codex-pool",
		})
	}
	body, err := json.Marshal(map[string]any{
		"object": "list",
		"data":   data,
	})
	if err != nil {
		body = []byte(`{"object":"list","data":[]}`)
	}
	return codexModelsCacheEntry{
		Body:        body,
		ContentType: "application/json",
		FetchedAt:   time.Now(),
	}
}
