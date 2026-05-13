package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSaveNewCodexAccountUsesSuffixBeyond99(t *testing.T) {
	poolDir := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	basePath := filepath.Join(poolDir, "workspac.json")
	basePayload := []byte(`{"tokens":{"id_token":"old"}}`)
	if err := os.WriteFile(basePath, basePayload, 0o600); err != nil {
		t.Fatalf("write base account: %v", err)
	}
	for i := 2; i <= 99; i++ {
		path := filepath.Join(poolDir, "workspac_"+strconv.Itoa(i)+".json")
		if err := os.WriteFile(path, []byte(`{"tokens":{"id_token":"old"}}`), 0o600); err != nil {
			t.Fatalf("write occupied suffix %d: %v", i, err)
		}
	}

	tokens := &CodexTokenResponse{
		IDToken:      testCodexIDToken(t, "user-new", "workspace-new", "workspace204.bartos@example.test", "sub-new", time.Now().Add(time.Hour)),
		AccessToken:  "access-new",
		RefreshToken: "refresh-new",
	}
	savedID, savedPath, refreshedExisting, err := saveNewCodexAccount(poolDir, "workspac", tokens)
	if err != nil {
		t.Fatalf("saveNewCodexAccount: %v", err)
	}
	if refreshedExisting {
		t.Fatal("did not expect existing logical seat refresh")
	}
	if savedID != "workspac_100" {
		t.Fatalf("savedID=%q, want workspac_100", savedID)
	}
	if filepath.Base(savedPath) != "workspac_100.json" {
		t.Fatalf("savedPath=%q", savedPath)
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("expected saved file: %v", err)
	}
	baseAfter, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if string(baseAfter) != string(basePayload) {
		t.Fatalf("base account was overwritten")
	}
}
