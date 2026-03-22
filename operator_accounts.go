package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

func (h *proxyHandler) handleOperatorAccountDelete(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&payload); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	accountID := strings.TrimSpace(payload.AccountID)
	if accountID == "" {
		respondJSONError(w, http.StatusBadRequest, "account_id is required")
		return
	}

	h.pool.mu.RLock()
	var target *Account
	for _, acc := range h.pool.accounts {
		if acc != nil && acc.ID == accountID {
			target = acc
			break
		}
	}
	h.pool.mu.RUnlock()

	if target == nil {
		respondJSONError(w, http.StatusNotFound, "account not found")
		return
	}

	filePath := strings.TrimSpace(target.File)
	if filePath == "" {
		respondJSONError(w, http.StatusInternalServerError, "account file path is empty")
		return
	}
	if atomic.LoadInt64(&target.Inflight) > 0 {
		respondJSONError(w, http.StatusConflict, "account has inflight requests")
		return
	}
	if err := ensureOperatorManagedPoolPath(h.cfg.poolDir, filePath); err != nil {
		respondJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	alreadyRemoved := false
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			alreadyRemoved = true
		} else {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	h.reloadAccounts()

	respondJSON(w, map[string]any{
		"status":          "ok",
		"account_id":      accountID,
		"provider":        string(target.Type),
		"auth_mode":       accountAuthMode(target),
		"fallback_only":   isManagedCodexAPIKeyAccount(target),
		"already_removed": alreadyRemoved,
	})
}

func ensureOperatorManagedPoolPath(poolDir, filePath string) error {
	absPool, err := filepath.Abs(strings.TrimSpace(poolDir))
	if err != nil {
		return fmt.Errorf("resolve pool dir: %w", err)
	}
	absFile, err := filepath.Abs(strings.TrimSpace(filePath))
	if err != nil {
		return fmt.Errorf("resolve account file: %w", err)
	}
	if filepath.Ext(absFile) != ".json" {
		return fmt.Errorf("account file is not a json file")
	}
	rel, err := filepath.Rel(absPool, absFile)
	if err != nil {
		return fmt.Errorf("resolve account file path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("account file is outside the pool directory")
	}
	return nil
}
