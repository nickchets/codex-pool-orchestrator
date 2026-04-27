package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

func validateOpenAICompatiblePoolKeyEndpoint(admission AdmissionResult, path string) error {
	if !isOpenAICompatiblePoolKeyAdmission(admission) {
		return nil
	}
	if isOpenAICompatibleEndpointPath(path) {
		return nil
	}
	return fmt.Errorf("openai-compatible pool API tokens are only valid for OpenAI-compatible endpoints")
}

func shouldForceBufferedOpenAICompatibleBody(admission AdmissionResult, path string) bool {
	return isOpenAICompatiblePoolKeyAdmission(admission) && isOpenAICompatibleModelRequestPath(path)
}

func isOpenAICompatibleEndpointPath(path string) bool {
	segments, ok := openAICompatiblePathSegments(path)
	if !ok {
		return false
	}
	return isOpenAICompatibleModelsPathSegments(segments) ||
		isOpenAICompatibleChatCompletionsPathSegments(segments) ||
		isOpenAICompatibleResponsesPathSegments(segments)
}

func isOpenAICompatibleModelRequestPath(path string) bool {
	segments, ok := openAICompatiblePathSegments(path)
	if !ok {
		return false
	}
	return isOpenAICompatibleChatCompletionsPathSegments(segments) || isOpenAICompatibleResponsesPathSegments(segments)
}

func openAICompatiblePathSegments(rawPath string) ([]string, bool) {
	path := strings.TrimSpace(rawPath)
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil, false
	}
	decodedPath, err := url.PathUnescape(path)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(decodedPath, "/")
	if len(parts) < 2 || parts[0] != "" {
		return nil, false
	}
	segments := parts[1:]
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, false
		}
	}
	return segments, true
}

func isOpenAICompatibleModelsPathSegments(segments []string) bool {
	return len(segments) == 2 && segments[0] == "v1" && segments[1] == "models"
}

func isOpenAICompatibleChatCompletionsPathSegments(segments []string) bool {
	return len(segments) == 3 && segments[0] == "v1" && segments[1] == "chat" && segments[2] == "completions"
}

func isOpenAICompatibleResponsesPathSegments(segments []string) bool {
	if len(segments) < 2 || segments[0] != "v1" || segments[1] != "responses" {
		return false
	}
	if len(segments) == 2 {
		return true
	}
	if !isSafeOpenAICompatibleResponsePathSegment(segments[2]) {
		return false
	}
	if len(segments) == 3 {
		return true
	}
	return len(segments) == 4 && isAllowedOpenAICompatibleResponseChildOperation(segments[3])
}

func isSafeOpenAICompatibleResponsePathSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func isAllowedOpenAICompatibleResponseChildOperation(segment string) bool {
	switch segment {
	case "cancel", "input_items":
		return true
	default:
		return false
	}
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

func hasRestrictiveOpenAICompatibleModelAllowlist(allowedModels []string) bool {
	patterns := cleanModelAllowlist(allowedModels)
	return len(patterns) > 0 && !allowlistContainsGlobalWildcard(patterns)
}

func validateOpenAICompatibleModelAllowlist(routePlan RoutePlan, requestBodyForInspection []byte) error {
	if !routePlan.IsOpenAICompatibleClient || !isOpenAICompatibleModelRequestPath(routePlan.Shape.Path) {
		return nil
	}
	model := strings.TrimSpace(routePlan.Shape.RequestedModel)
	if model == "" && len(requestBodyForInspection) > 0 && hasRestrictiveOpenAICompatibleModelAllowlist(routePlan.Admission.TokenAllowedModels) {
		return fmt.Errorf("request model could not be inspected for this pool API token")
	}
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
