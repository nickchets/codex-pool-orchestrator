package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newOperatorGeminiResetTestHandler(t *testing.T, poolDir string, accounts []*Account) *proxyHandler {
	t.Helper()
	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	return &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(accounts, false),
		registry: NewProviderRegistry(NewCodexProvider(apiBase, apiBase, apiBase, apiBase), NewClaudeProvider(apiBase), NewGeminiProvider(apiBase, apiBase)),
	}
}

func writeOperatorGeminiResetFixture(t *testing.T, poolDir string, acc *Account) {
	t.Helper()
	if acc.Type == "" {
		acc.Type = AccountTypeGemini
	}
	if acc.PlanType == "" {
		acc.PlanType = "gemini"
	}
	if strings.TrimSpace(acc.RefreshToken) == "" {
		acc.RefreshToken = "refresh-" + acc.ID
	}
	if strings.TrimSpace(acc.File) == "" {
		acc.File = filepath.Join(poolDir, managedGeminiSubdir, acc.ID+".json")
	}
	if err := os.MkdirAll(filepath.Dir(acc.File), 0o755); err != nil {
		t.Fatalf("mkdir gemini dir: %v", err)
	}
	raw := `{"access_token":"seed-access","refresh_token":"` + acc.RefreshToken + `"}`
	if err := os.WriteFile(acc.File, []byte(raw), 0o600); err != nil {
		t.Fatalf("write seed gemini file: %v", err)
	}
	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount: %v", err)
	}
}

func loopbackRequest(method, target string, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestOperatorGeminiResetBundleCreatesBackupAndSanitizedInventory(t *testing.T) {
	poolDir := t.TempDir()
	now := time.Now().UTC()
	ready := &Account{
		ID:                      "gemini-seat-ready",
		Type:                    AccountTypeGemini,
		RefreshToken:            "refresh-ready",
		OperatorEmail:           "ready@example.com",
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		OAuthProfileID:          geminiOAuthAntigravityProfileID,
		AntigravityProjectID:    "project-ready",
		GeminiProviderCheckedAt: now,
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
	}
	blocked := &Account{
		ID:                       "gemini-seat-missing-project",
		Type:                     AccountTypeGemini,
		RefreshToken:             "refresh-missing",
		OperatorEmail:            "missing@example.com",
		OperatorSource:           geminiOperatorSourceAntigravityImport,
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		GeminiProviderCheckedAt:  now,
		GeminiProviderTruthState: geminiProviderTruthStateMissingProjectID,
	}
	writeOperatorGeminiResetFixture(t, poolDir, ready)
	writeOperatorGeminiResetFixture(t, poolDir, blocked)

	h := newOperatorGeminiResetTestHandler(t, poolDir, []*Account{ready, blocked})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-bundle", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(payload["seat_count"].(float64)); got != 2 {
		t.Fatalf("seat_count=%d", got)
	}
	bundleDir := payload["bundle_dir"].(string)
	inventoryPath := payload["inventory_path"].(string)
	beforeStatusPath := payload["before_status_path"].(string)
	manifestPath := payload["manifest_path"].(string)

	for _, path := range []string{bundleDir, inventoryPath, beforeStatusPath, manifestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	inventoryRaw, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if strings.Contains(string(inventoryRaw), "refresh-ready") || strings.Contains(string(inventoryRaw), "refresh-missing") {
		t.Fatalf("inventory leaked refresh token: %s", inventoryRaw)
	}

	backupRaw, err := os.ReadFile(filepath.Join(bundleDir, "backups", "gemini-seat-ready.json"))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(backupRaw), "refresh-ready") {
		t.Fatalf("backup missing raw refresh token: %s", backupRaw)
	}

	beforeStatusRaw, err := os.ReadFile(beforeStatusPath)
	if err != nil {
		t.Fatalf("read before_status: %v", err)
	}
	if !strings.Contains(string(beforeStatusRaw), "\"gemini_pool\"") {
		t.Fatalf("before_status missing gemini_pool: %s", beforeStatusRaw)
	}
}

func TestOperatorGeminiResetDeleteAndRollback(t *testing.T) {
	poolDir := t.TempDir()
	now := time.Now().UTC()
	ready := &Account{
		ID:                      "gemini-seat-ready",
		Type:                    AccountTypeGemini,
		RefreshToken:            "refresh-ready",
		OperatorEmail:           "ready@example.com",
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		OAuthProfileID:          geminiOAuthAntigravityProfileID,
		AntigravityProjectID:    "project-ready",
		GeminiProviderCheckedAt: now,
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
	}
	writeOperatorGeminiResetFixture(t, poolDir, ready)

	h := newOperatorGeminiResetTestHandler(t, poolDir, []*Account{ready})

	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-bundle", `{}`))
	if createRec.Code != http.StatusOK {
		t.Fatalf("bundle status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode bundle response: %v", err)
	}
	bundleID := createPayload["bundle_id"].(string)

	deleteRec := httptest.NewRecorder()
	h.ServeHTTP(deleteRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-delete", `{"bundle_id":"`+bundleID+`"}`))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(ready.File); !os.IsNotExist(err) {
		t.Fatalf("expected seat file to be removed, stat err=%v", err)
	}
	if got := h.pool.countByType(AccountTypeGemini); got != 0 {
		t.Fatalf("gemini seats after delete=%d", got)
	}

	rollbackRec := httptest.NewRecorder()
	h.ServeHTTP(rollbackRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-rollback", `{"bundle_id":"`+bundleID+`"}`))
	if rollbackRec.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rollbackRec.Code, rollbackRec.Body.String())
	}
	if _, err := os.Stat(ready.File); err != nil {
		t.Fatalf("expected seat file restored: %v", err)
	}
	if got := h.pool.countByType(AccountTypeGemini); got != 1 {
		t.Fatalf("gemini seats after rollback=%d", got)
	}

	bundleDir, err := operatorGeminiResetBundleDir(poolDir, bundleID)
	if err != nil {
		t.Fatalf("bundle dir: %v", err)
	}
	for _, path := range []string{
		filepath.Join(bundleDir, "after_delete_status.json"),
		filepath.Join(bundleDir, "rollback_status.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}
}

func TestOperatorGeminiResetDeleteRejectsManifestTraversal(t *testing.T) {
	poolDir := t.TempDir()
	now := time.Now().UTC()
	ready := &Account{
		ID:                      "gemini-seat-ready",
		Type:                    AccountTypeGemini,
		RefreshToken:            "refresh-ready",
		OperatorEmail:           "ready@example.com",
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		OAuthProfileID:          geminiOAuthAntigravityProfileID,
		AntigravityProjectID:    "project-ready",
		GeminiProviderCheckedAt: now,
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
	}
	writeOperatorGeminiResetFixture(t, poolDir, ready)

	h := newOperatorGeminiResetTestHandler(t, poolDir, []*Account{ready})

	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-bundle", `{}`))
	if createRec.Code != http.StatusOK {
		t.Fatalf("bundle status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode bundle response: %v", err)
	}
	bundleID := createPayload["bundle_id"].(string)
	bundleDir, err := operatorGeminiResetBundleDir(poolDir, bundleID)
	if err != nil {
		t.Fatalf("bundle dir: %v", err)
	}

	manifestPath := filepath.Join(bundleDir, "manifest.json")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest operatorGeminiResetManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Seats) != 1 {
		t.Fatalf("seat count=%d", len(manifest.Seats))
	}
	outsideFile := filepath.Join(filepath.Dir(poolDir), "escape-delete.json")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	manifest.Seats[0].RelativeFile = filepath.Join("..", filepath.Base(outsideFile))
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	deleteRec := httptest.NewRecorder()
	h.ServeHTTP(deleteRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-delete", `{"bundle_id":"`+bundleID+`"}`))
	if deleteRec.Code != http.StatusForbidden {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside file should remain: %v", err)
	}
	if _, err := os.Stat(ready.File); err != nil {
		t.Fatalf("managed seat file should remain: %v", err)
	}
	if got := h.pool.countByType(AccountTypeGemini); got != 1 {
		t.Fatalf("gemini seats after rejected delete=%d", got)
	}
}

func TestOperatorGeminiResetRollbackRejectsManifestTraversal(t *testing.T) {
	poolDir := t.TempDir()
	now := time.Now().UTC()
	ready := &Account{
		ID:                      "gemini-seat-ready",
		Type:                    AccountTypeGemini,
		RefreshToken:            "refresh-ready",
		OperatorEmail:           "ready@example.com",
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		OAuthProfileID:          geminiOAuthAntigravityProfileID,
		AntigravityProjectID:    "project-ready",
		GeminiProviderCheckedAt: now,
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
	}
	writeOperatorGeminiResetFixture(t, poolDir, ready)

	h := newOperatorGeminiResetTestHandler(t, poolDir, []*Account{ready})

	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-bundle", `{}`))
	if createRec.Code != http.StatusOK {
		t.Fatalf("bundle status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode bundle response: %v", err)
	}
	bundleID := createPayload["bundle_id"].(string)
	bundleDir, err := operatorGeminiResetBundleDir(poolDir, bundleID)
	if err != nil {
		t.Fatalf("bundle dir: %v", err)
	}

	manifestPath := filepath.Join(bundleDir, "manifest.json")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest operatorGeminiResetManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Seats) != 1 {
		t.Fatalf("seat count=%d", len(manifest.Seats))
	}
	outsideFile := filepath.Join(filepath.Dir(poolDir), "escape-rollback.json")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	manifest.Seats[0].RelativeFile = filepath.Join("..", filepath.Base(outsideFile))
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	rollbackRec := httptest.NewRecorder()
	h.ServeHTTP(rollbackRec, loopbackRequest(http.MethodPost, "http://127.0.0.1:8989/operator/gemini/reset-rollback", `{"bundle_id":"`+bundleID+`"}`))
	if rollbackRec.Code != http.StatusForbidden {
		t.Fatalf("rollback status=%d body=%s", rollbackRec.Code, rollbackRec.Body.String())
	}
	outsideRaw, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(outsideRaw) != "keep" {
		t.Fatalf("outside file changed: %q", string(outsideRaw))
	}
}
