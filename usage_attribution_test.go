package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func testUsageStore(t *testing.T) *usageStore {
	t.Helper()
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 30)
	if err != nil {
		t.Fatalf("open usage store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func readStoredRequestUsageRows(t *testing.T, store *usageStore) []RequestUsage {
	t.Helper()
	var rows []RequestUsage
	err := store.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketUsageRequests)).ForEach(func(_, v []byte) error {
			var row RequestUsage
			if err := json.Unmarshal(v, &row); err != nil {
				return err
			}
			rows = append(rows, row)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("read usage rows: %v", err)
	}
	return rows
}

func assertUsageBucketsDoNotContain(t *testing.T, store *usageStore, needle string, buckets ...string) {
	t.Helper()
	err := store.db.View(func(tx *bbolt.Tx) error {
		for _, bucketName := range buckets {
			bucket := tx.Bucket([]byte(bucketName))
			if bucket == nil {
				t.Fatalf("bucket %s missing", bucketName)
			}
			if err := bucket.ForEach(func(k, v []byte) error {
				if strings.Contains(string(k), needle) || strings.Contains(string(v), needle) {
					t.Fatalf("raw API key leaked in bucket %s key=%q value=%s", bucketName, string(k), string(v))
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan usage buckets: %v", err)
	}
}

func TestUsageStoreRecordsTokenAttributionAggregates(t *testing.T) {
	store := testUsageStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	ru := RequestUsage{
		Timestamp:         now,
		AccountID:         "seat-1",
		PlanType:          "team",
		UserID:            "user-1",
		TokenID:           "tok-1",
		TokenName:         "ci key",
		CredentialKind:    CredentialKindOpenAICompatiblePoolKey,
		ClientEndpoint:    "/v1/responses",
		Stream:            false,
		Status:            http.StatusOK,
		InputTokens:       100,
		CachedInputTokens: 20,
		OutputTokens:      5,
		ReasoningTokens:   2,
		BillableTokens:    85,
		RequestID:         "req-token-1",
		AccountType:       AccountTypeCodex,
	}
	if err := store.record(ru); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	acct, err := store.loadAccountUsage("seat-1")
	if err != nil {
		t.Fatalf("load account usage: %v", err)
	}
	if acct.TotalBillableTokens != 85 || acct.TotalInputTokens != 100 || acct.RequestCount != 1 {
		t.Fatalf("account aggregate = %+v", acct)
	}

	users, err := store.getAllUserUsage()
	if err != nil {
		t.Fatalf("load user usage: %v", err)
	}
	if len(users) != 1 || users[0].UserID != "user-1" || users[0].TotalBillableTokens != 85 || users[0].RequestCount != 1 {
		t.Fatalf("user aggregate = %+v", users)
	}

	tok, err := store.getTokenUsage("tok-1")
	if err != nil {
		t.Fatalf("load token usage: %v", err)
	}
	if tok.TokenID != "tok-1" || tok.TokenName != "ci key" || tok.UserID != "user-1" {
		t.Fatalf("token identity aggregate = %+v", tok)
	}
	if tok.CredentialKind != CredentialKindOpenAICompatiblePoolKey || tok.LastClientEndpoint != "/v1/responses" || tok.LastStatus != http.StatusOK {
		t.Fatalf("token attribution aggregate = %+v", tok)
	}
	if tok.TotalBillableTokens != 85 || tok.TotalInputTokens != 100 || tok.TotalCachedTokens != 20 || tok.TotalOutputTokens != 5 || tok.TotalReasoningTokens != 2 || tok.RequestCount != 1 {
		t.Fatalf("token token totals = %+v", tok)
	}

	byUser, err := store.listTokenUsageByUser("user-1")
	if err != nil {
		t.Fatalf("list token usage by user: %v", err)
	}
	if len(byUser) != 1 || byUser[0].TokenID != "tok-1" {
		t.Fatalf("token usage by user = %+v", byUser)
	}
}

func TestUsageStoreSkipsTokenAggregateWhenTokenIDEmpty(t *testing.T) {
	store := testUsageStore(t)

	if err := store.record(RequestUsage{
		Timestamp:      time.Now().UTC(),
		AccountID:      "seat-1",
		UserID:         "user-1",
		InputTokens:    8,
		OutputTokens:   2,
		BillableTokens: 10,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	byUser, err := store.listTokenUsageByUser("user-1")
	if err != nil {
		t.Fatalf("list token usage by user: %v", err)
	}
	if len(byUser) != 0 {
		t.Fatalf("expected no token aggregate for empty TokenID, got %+v", byUser)
	}

	acct, err := store.loadAccountUsage("seat-1")
	if err != nil {
		t.Fatalf("load account usage: %v", err)
	}
	if acct.TotalBillableTokens != 10 || acct.RequestCount != 1 {
		t.Fatalf("account aggregate should still be recorded: %+v", acct)
	}
}

func TestBuildUsageAttributionFromVirtualKey(t *testing.T) {
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-1",
		TokenID:        "tok-1",
		TokenName:      "ci key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/chat/completions", true)

	if attr.TokenID != "tok-1" || attr.TokenName != "ci key" || attr.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("token attribution = %+v", attr)
	}
	if attr.ClientEndpoint != "/v1/chat/completions" || !attr.Stream {
		t.Fatalf("request attribution = %+v", attr)
	}

	passthrough := buildUsageAttribution(AdmissionResult{Kind: AdmissionKindPassthrough}, "/v1/responses", false)
	if passthrough.TokenID != "" || passthrough.TokenName != "" || passthrough.CredentialKind != "" {
		t.Fatalf("passthrough should not carry token attribution: %+v", passthrough)
	}
}

func TestUpdateUsageFromBodyAppliesVirtualKeyAttribution(t *testing.T) {
	store := testUsageStore(t)
	baseURL, _ := url.Parse("https://api.openai.example")
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	h := &proxyHandler{store: store}
	acc := &Account{ID: "codex-seat", Type: AccountTypeCodex, PlanType: "team"}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-1",
		TokenID:        "tok-json",
		TokenName:      "json key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", false)
	attr.Status = http.StatusOK

	h.updateUsageFromBodyWithAttribution(provider, acc, "user-1", 0, 0, []byte(`{"id":"resp_1","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":4}}`), attr)

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	row := rows[0]
	if row.TokenID != "tok-json" || row.TokenName != "json key" || row.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("row token attribution = %+v", row)
	}
	if row.ClientEndpoint != "/v1/responses" || row.Stream || row.Status != http.StatusOK {
		t.Fatalf("row request attribution = %+v", row)
	}

	tok, err := store.getTokenUsage("tok-json")
	if err != nil {
		t.Fatalf("get token usage: %v", err)
	}
	if tok.TotalBillableTokens != 12 || tok.RequestCount != 1 {
		t.Fatalf("token aggregate = %+v", tok)
	}
}

func TestWrapUsageInterceptWriterAppliesVirtualKeyAttributionOnce(t *testing.T) {
	store := testUsageStore(t)
	baseURL, _ := url.Parse("https://api.openai.example")
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	h := &proxyHandler{store: store}
	acc := &Account{ID: "codex-seat", Type: AccountTypeCodex, PlanType: "team"}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-1",
		TokenID:        "tok-stream",
		TokenName:      "stream key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", true)
	attr.Status = http.StatusOK

	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once
	var forwarded bytes.Buffer
	writer := h.wrapUsageInterceptWriterWithAttribution(
		"req-stream",
		&forwarded,
		provider,
		acc,
		"user-1",
		nil,
		0,
		0,
		&managedStreamFailed,
		&managedStreamFailureOnce,
		attr,
	)

	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"usage\":{\"input_tokens\":11,\"cached_input_tokens\":1,\"output_tokens\":5}}\n\nevent: done\ndata: [DONE]\n\n")
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write sse chunk: %v", err)
	}

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("expected one usage row, got %+v", rows)
	}
	row := rows[0]
	if row.TokenID != "tok-stream" || row.TokenName != "stream key" || row.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("row token attribution = %+v", row)
	}
	if row.ClientEndpoint != "/v1/responses" || !row.Stream || row.Status != http.StatusOK {
		t.Fatalf("row request attribution = %+v", row)
	}

	tok, err := store.getTokenUsage("tok-stream")
	if err != nil {
		t.Fatalf("get token usage: %v", err)
	}
	if tok.TotalBillableTokens != 15 || tok.RequestCount != 1 || tok.StreamRequestCount != 1 {
		t.Fatalf("token aggregate = %+v", tok)
	}
}

func TestUsageAttributionDoesNotPersistRawPoolAPIKey(t *testing.T) {
	store := testUsageStore(t)
	rawKey := "sk-" + "cpool-" + "rawkid.rawsecret"
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-1",
		TokenID:        "rawkid",
		TokenName:      "raw safety key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", false)
	attr.Status = http.StatusOK

	ru := applyUsageAttribution(&RequestUsage{
		Timestamp:      time.Now().UTC(),
		AccountID:      "seat-1",
		UserID:         "user-1",
		InputTokens:    1,
		OutputTokens:   1,
		BillableTokens: 2,
	}, attr)
	if err := store.record(*ru); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	assertUsageBucketsDoNotContain(t, store, rawKey, bucketUsageRequests, bucketTokenUsage, bucketTokenDailyUsage, bucketTokenHourlyUsage)
}
