package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const operatorGeminiResetArtifactsSubdir = "operator_artifacts/gemini_reset"

type operatorGeminiResetSeatManifest struct {
	AccountID          string `json:"account_id"`
	RelativeFile       string `json:"relative_file"`
	BackupFile         string `json:"backup_file"`
	Email              string `json:"email,omitempty"`
	OperatorSource     string `json:"operator_source,omitempty"`
	HealthStatus       string `json:"health_status,omitempty"`
	ProviderTruthState string `json:"provider_truth_state,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	RoutingState       string `json:"routing_state,omitempty"`
	Eligible           bool   `json:"eligible"`
}

type operatorGeminiResetManifest struct {
	BundleID              string                            `json:"bundle_id"`
	CreatedAt             string                            `json:"created_at"`
	InventoryPath         string                            `json:"inventory_path"`
	BeforeStatusPath      string                            `json:"before_status_path"`
	AfterDeleteStatusPath string                            `json:"after_delete_status_path,omitempty"`
	RollbackStatusPath    string                            `json:"rollback_status_path,omitempty"`
	SeatCount             int                               `json:"seat_count"`
	Seats                 []operatorGeminiResetSeatManifest `json:"seats"`
}

type operatorGeminiResetInventory struct {
	GeneratedAt string                            `json:"generated_at"`
	SeatCount   int                               `json:"seat_count"`
	Seats       []operatorGeminiResetSeatManifest `json:"seats"`
}

type operatorGeminiResetStatusSnapshot struct {
	GeneratedAt    time.Time            `json:"generated_at"`
	GeminiPool     *GeminiPoolStatus    `json:"gemini_pool,omitempty"`
	GeminiOperator GeminiOperatorStatus `json:"gemini_operator"`
	Accounts       []AccountStatus      `json:"accounts"`
}

type operatorGeminiResetRequest struct {
	BundleID string `json:"bundle_id"`
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func copyFileContents(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Close()
}

func operatorGeminiResetBundleDir(poolDir, bundleID string) (string, error) {
	absPool, err := filepath.Abs(strings.TrimSpace(poolDir))
	if err != nil {
		return "", fmt.Errorf("resolve pool dir: %w", err)
	}
	return filepath.Join(absPool, operatorGeminiResetArtifactsSubdir, bundleID), nil
}

func (h *proxyHandler) buildOperatorGeminiResetStatusSnapshot(now time.Time) operatorGeminiResetStatusSnapshot {
	data := h.buildPoolDashboardData(now)
	filtered := make([]AccountStatus, 0, len(data.Accounts))
	for _, account := range data.Accounts {
		if account.Type == string(AccountTypeGemini) {
			filtered = append(filtered, account)
		}
	}
	return operatorGeminiResetStatusSnapshot{
		GeneratedAt:    data.GeneratedAt,
		GeminiPool:     data.GeminiPool,
		GeminiOperator: data.GeminiOperator,
		Accounts:       filtered,
	}
}

func (h *proxyHandler) collectOperatorGeminiResetSeats(now time.Time) ([]operatorGeminiResetSeatManifest, operatorGeminiResetStatusSnapshot, error) {
	statusSnapshot := h.buildOperatorGeminiResetStatusSnapshot(now)
	statusByID := make(map[string]AccountStatus, len(statusSnapshot.Accounts))
	for _, account := range statusSnapshot.Accounts {
		statusByID[account.ID] = account
	}

	absPool, err := filepath.Abs(strings.TrimSpace(h.cfg.poolDir))
	if err != nil {
		return nil, operatorGeminiResetStatusSnapshot{}, fmt.Errorf("resolve pool dir: %w", err)
	}

	h.pool.mu.RLock()
	accounts := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	seats := make([]operatorGeminiResetSeatManifest, 0, len(accounts))
	for _, account := range accounts {
		if account == nil || account.Type != AccountTypeGemini {
			continue
		}
		account.mu.Lock()
		filePath := strings.TrimSpace(account.File)
		accountID := strings.TrimSpace(account.ID)
		account.mu.Unlock()
		if filePath == "" || accountID == "" {
			continue
		}
		if err := ensureOperatorManagedPoolPath(h.cfg.poolDir, filePath); err != nil {
			return nil, operatorGeminiResetStatusSnapshot{}, err
		}
		absFile, err := filepath.Abs(filePath)
		if err != nil {
			return nil, operatorGeminiResetStatusSnapshot{}, fmt.Errorf("resolve account file: %w", err)
		}
		relFile, err := filepath.Rel(absPool, absFile)
		if err != nil {
			return nil, operatorGeminiResetStatusSnapshot{}, fmt.Errorf("resolve relative account path: %w", err)
		}
		status := statusByID[accountID]
		seat := operatorGeminiResetSeatManifest{
			AccountID:      accountID,
			RelativeFile:   relFile,
			BackupFile:     filepath.Join("backups", accountID+".json"),
			Email:          status.Email,
			OperatorSource: status.OperatorSource,
			HealthStatus:   status.HealthStatus,
			ProviderTruthState: func() string {
				if status.ProviderTruth != nil {
					return status.ProviderTruth.State
				}
				return ""
			}(),
			ProjectID: func() string {
				if status.ProviderTruth != nil {
					return status.ProviderTruth.ProjectID
				}
				return ""
			}(),
			RoutingState: firstNonEmpty(status.Routing.State, status.Routing.BlockReason),
			Eligible:     status.Routing.Eligible,
		}
		seats = append(seats, seat)
	}
	sort.Slice(seats, func(i, j int) bool {
		return seats[i].AccountID < seats[j].AccountID
	})
	if len(seats) == 0 {
		return nil, operatorGeminiResetStatusSnapshot{}, fmt.Errorf("no Gemini seats are present in the pool")
	}
	return seats, statusSnapshot, nil
}

func readOperatorGeminiResetRequest(r *http.Request) (operatorGeminiResetRequest, error) {
	var payload operatorGeminiResetRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&payload); err != nil {
		return payload, err
	}
	payload.BundleID = strings.TrimSpace(payload.BundleID)
	if payload.BundleID == "" {
		return payload, fmt.Errorf("bundle_id is required")
	}
	return payload, nil
}

func (h *proxyHandler) loadOperatorGeminiResetManifest(bundleID string) (operatorGeminiResetManifest, string, error) {
	var manifest operatorGeminiResetManifest
	bundleDir, err := operatorGeminiResetBundleDir(h.cfg.poolDir, bundleID)
	if err != nil {
		return manifest, "", err
	}
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return manifest, "", err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, "", err
	}
	return manifest, manifestPath, nil
}

func (h *proxyHandler) handleOperatorGeminiResetBundle(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	seats, statusSnapshot, err := h.collectOperatorGeminiResetSeats(now)
	if err != nil {
		respondJSONError(w, http.StatusConflict, err.Error())
		return
	}

	bundleID := "gemini_reset_" + now.Format("20060102T150405Z")
	bundleDir, err := operatorGeminiResetBundleDir(h.cfg.poolDir, bundleID)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	backupsDir := filepath.Join(bundleDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	absPool, err := filepath.Abs(strings.TrimSpace(h.cfg.poolDir))
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, seat := range seats {
		sourcePath := filepath.Join(absPool, seat.RelativeFile)
		backupPath := filepath.Join(bundleDir, seat.BackupFile)
		if err := copyFileContents(sourcePath, backupPath); err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	inventory := operatorGeminiResetInventory{
		GeneratedAt: now.Format(time.RFC3339),
		SeatCount:   len(seats),
		Seats:       seats,
	}
	beforeStatusPath := filepath.Join(bundleDir, "before_status.json")
	inventoryPath := filepath.Join(bundleDir, "inventory.json")
	manifest := operatorGeminiResetManifest{
		BundleID:         bundleID,
		CreatedAt:        now.Format(time.RFC3339),
		InventoryPath:    inventoryPath,
		BeforeStatusPath: beforeStatusPath,
		SeatCount:        len(seats),
		Seats:            seats,
	}
	if err := writeJSONFile(inventoryPath, inventory); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeJSONFile(beforeStatusPath, statusSnapshot); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, map[string]any{
		"status":             "ok",
		"bundle_id":          bundleID,
		"bundle_dir":         bundleDir,
		"seat_count":         len(seats),
		"manifest_path":      manifestPath,
		"inventory_path":     inventoryPath,
		"before_status_path": beforeStatusPath,
	})
}

func (h *proxyHandler) handleOperatorGeminiResetDelete(w http.ResponseWriter, r *http.Request) {
	payload, err := readOperatorGeminiResetRequest(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	manifest, manifestPath, err := h.loadOperatorGeminiResetManifest(payload.BundleID)
	if err != nil {
		respondJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	h.pool.mu.RLock()
	currentAccounts := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()
	currentByID := make(map[string]*Account, len(currentAccounts))
	for _, account := range currentAccounts {
		if account != nil {
			currentByID[account.ID] = account
		}
	}

	absPool, err := filepath.Abs(strings.TrimSpace(h.cfg.poolDir))
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	deletedCount := 0
	alreadyRemoved := 0
	for _, seat := range manifest.Seats {
		if account := currentByID[seat.AccountID]; account != nil {
			if atomic.LoadInt64(&account.Inflight) > 0 {
				respondJSONError(w, http.StatusConflict, "Gemini seat has inflight requests: "+seat.AccountID)
				return
			}
		}
		filePath := filepath.Join(absPool, seat.RelativeFile)
		if err := ensureOperatorManagedPoolPath(h.cfg.poolDir, filePath); err != nil {
			respondJSONError(w, http.StatusForbidden, err.Error())
			return
		}
		if err := os.Remove(filePath); err != nil {
			if os.IsNotExist(err) {
				alreadyRemoved++
				continue
			}
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		deletedCount++
	}

	h.reloadAccounts()
	afterDeletePath := filepath.Join(filepath.Dir(manifestPath), "after_delete_status.json")
	statusSnapshot := h.buildOperatorGeminiResetStatusSnapshot(time.Now().UTC())
	if err := writeJSONFile(afterDeletePath, statusSnapshot); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	manifest.AfterDeleteStatusPath = afterDeletePath
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, map[string]any{
		"status":                 "ok",
		"bundle_id":              manifest.BundleID,
		"deleted_count":          deletedCount,
		"already_removed_count":  alreadyRemoved,
		"after_delete_status":    afterDeletePath,
		"remaining_gemini_seats": h.pool.countByType(AccountTypeGemini),
	})
}

func (h *proxyHandler) handleOperatorGeminiResetRollback(w http.ResponseWriter, r *http.Request) {
	payload, err := readOperatorGeminiResetRequest(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	manifest, manifestPath, err := h.loadOperatorGeminiResetManifest(payload.BundleID)
	if err != nil {
		respondJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	bundleDir := filepath.Dir(manifestPath)

	absPool, err := filepath.Abs(strings.TrimSpace(h.cfg.poolDir))
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	restoredCount := 0
	for _, seat := range manifest.Seats {
		sourcePath := filepath.Join(bundleDir, seat.BackupFile)
		targetPath := filepath.Join(absPool, seat.RelativeFile)
		if err := ensureOperatorManagedPoolPath(h.cfg.poolDir, targetPath); err != nil {
			respondJSONError(w, http.StatusForbidden, err.Error())
			return
		}
		if err := copyFileContents(sourcePath, targetPath); err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		restoredCount++
	}

	h.reloadAccounts()
	rollbackStatusPath := filepath.Join(bundleDir, "rollback_status.json")
	statusSnapshot := h.buildOperatorGeminiResetStatusSnapshot(time.Now().UTC())
	if err := writeJSONFile(rollbackStatusPath, statusSnapshot); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	manifest.RollbackStatusPath = rollbackStatusPath
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, map[string]any{
		"status":                "ok",
		"bundle_id":             manifest.BundleID,
		"restored_count":        restoredCount,
		"rollback_status":       rollbackStatusPath,
		"restored_gemini_seats": h.pool.countByType(AccountTypeGemini),
	})
}
