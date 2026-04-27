package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestUsageStoreRecordAndAggregate(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "proxy.db")
	s, err := newUsageStore(path, 30)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ru := RequestUsage{AccountID: "acct1", InputTokens: 100, CachedInputTokens: 20, OutputTokens: 5, BillableTokens: 85, Timestamp: time.Now(), RequestID: "req1"}
	if err := s.record(ru); err != nil {
		t.Fatalf("record: %v", err)
	}

	agg, err := s.loadAccountUsage("acct1")
	if err != nil {
		t.Fatalf("load aggregate: %v", err)
	}
	if agg.TotalBillableTokens != 85 || agg.TotalInputTokens != 100 {
		t.Fatalf("unexpected aggregate: %+v", agg)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("db not created")
	}
}

func TestUsageStorePrune(t *testing.T) {
	s, err := newUsageStore(filepath.Join(t.TempDir(), "db.db"), 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	old := time.Now().Add(-48 * time.Hour)
	s.record(RequestUsage{AccountID: "acct", BillableTokens: 1, Timestamp: old})
	s.record(RequestUsage{AccountID: "acct", BillableTokens: 1, Timestamp: time.Now()})
	// Force prune
	s.nextPrune = time.Now().Add(-time.Hour)
	_ = s.record(RequestUsage{AccountID: "acct", BillableTokens: 1, Timestamp: time.Now()})

	err = s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketUsageRequests)).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if strings.Contains(string(k), fmt.Sprintf("%d", old.UnixNano())) {
				t.Fatalf("old entry not pruned")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestPurgeNonPoolUsersRemovesTokenUsageBuckets(t *testing.T) {
	s, err := newUsageStore(filepath.Join(t.TempDir(), "db.db"), 30)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, usage := range []RequestUsage{
		{
			Timestamp:      now,
			AccountID:      "seat-allowed",
			UserID:         "user-allowed",
			TokenID:        "tok-allowed",
			TokenName:      "allowed key",
			AccountType:    AccountTypeCodex,
			InputTokens:    10,
			OutputTokens:   2,
			BillableTokens: 12,
		},
		{
			Timestamp:      now,
			AccountID:      "seat-disallowed",
			UserID:         "user-disallowed",
			TokenID:        "tok-disallowed",
			TokenName:      "disallowed key",
			AccountType:    AccountTypeCodex,
			InputTokens:    20,
			OutputTokens:   3,
			BillableTokens: 23,
		},
	} {
		if err := s.record(usage); err != nil {
			t.Fatalf("record usage for %s: %v", usage.UserID, err)
		}
	}

	deleted, err := s.purgeNonPoolUsers(map[string]bool{"user-allowed": true})
	if err != nil {
		t.Fatalf("purge non-pool users: %v", err)
	}
	if deleted != 6 {
		t.Fatalf("deleted rows = %d, want 6 (user + token aggregate rows for disallowed user)", deleted)
	}

	allowedToken, err := s.getTokenUsage("tok-allowed")
	if err != nil {
		t.Fatalf("get allowed token usage: %v", err)
	}
	if allowedToken.TokenID != "tok-allowed" || allowedToken.UserID != "user-allowed" || allowedToken.TotalBillableTokens != 12 {
		t.Fatalf("allowed token aggregate was not retained: %+v", allowedToken)
	}

	disallowedToken, err := s.getTokenUsage("tok-disallowed")
	if err != nil {
		t.Fatalf("get disallowed token usage: %v", err)
	}
	if disallowedToken.TokenID != "" || disallowedToken.RequestCount != 0 || disallowedToken.TotalBillableTokens != 0 {
		t.Fatalf("disallowed token aggregate retained: %+v", disallowedToken)
	}

	allowedDaily, err := s.getTokenDailyUsage("tok-allowed", 2)
	if err != nil {
		t.Fatalf("get allowed daily token usage: %v", err)
	}
	if len(allowedDaily) != 1 || allowedDaily[0].UserID != "user-allowed" || allowedDaily[0].BillableTokens != 12 {
		t.Fatalf("allowed daily token aggregate was not retained: %+v", allowedDaily)
	}
	disallowedDaily, err := s.getTokenDailyUsage("tok-disallowed", 2)
	if err != nil {
		t.Fatalf("get disallowed daily token usage: %v", err)
	}
	if len(disallowedDaily) != 0 {
		t.Fatalf("disallowed daily token aggregates retained: %+v", disallowedDaily)
	}

	allowedHourly, err := s.getTokenHourlyUsage("tok-allowed", 2)
	if err != nil {
		t.Fatalf("get allowed hourly token usage: %v", err)
	}
	if len(allowedHourly) != 1 || allowedHourly[0].UserID != "user-allowed" || allowedHourly[0].BillableTokens != 12 {
		t.Fatalf("allowed hourly token aggregate was not retained: %+v", allowedHourly)
	}
	disallowedHourly, err := s.getTokenHourlyUsage("tok-disallowed", 2)
	if err != nil {
		t.Fatalf("get disallowed hourly token usage: %v", err)
	}
	if len(disallowedHourly) != 0 {
		t.Fatalf("disallowed hourly token aggregates retained: %+v", disallowedHourly)
	}
}

func TestPurgeNonPoolUsersInfersTokenBucketOwnerFromTokenUsage(t *testing.T) {
	s, err := newUsageStore(filepath.Join(t.TempDir(), "db.db"), 30)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, usage := range []RequestUsage{
		{
			Timestamp:      now,
			AccountID:      "seat-allowed",
			UserID:         "user-allowed",
			TokenID:        "tok-allowed-legacy",
			AccountType:    AccountTypeCodex,
			InputTokens:    5,
			OutputTokens:   1,
			BillableTokens: 6,
		},
		{
			Timestamp:      now,
			AccountID:      "seat-disallowed",
			UserID:         "user-disallowed",
			TokenID:        "tok-disallowed-legacy",
			AccountType:    AccountTypeCodex,
			InputTokens:    7,
			OutputTokens:   2,
			BillableTokens: 9,
		},
	} {
		if err := s.record(usage); err != nil {
			t.Fatalf("record usage for %s: %v", usage.UserID, err)
		}
	}

	removeJSONFieldFromBucketValue(t, s, bucketTokenDailyUsage, "tok-allowed-legacy|"+now.Format("2006-01-02"), "user_id")
	removeJSONFieldFromBucketValue(t, s, bucketTokenHourlyUsage, "tok-allowed-legacy|"+now.Format("2006-01-02T15"), "user_id")
	removeJSONFieldFromBucketValue(t, s, bucketTokenDailyUsage, "tok-disallowed-legacy|"+now.Format("2006-01-02"), "user_id")
	removeJSONFieldFromBucketValue(t, s, bucketTokenHourlyUsage, "tok-disallowed-legacy|"+now.Format("2006-01-02T15"), "user_id")

	deleted, err := s.purgeNonPoolUsers(map[string]bool{"user-allowed": true})
	if err != nil {
		t.Fatalf("purge non-pool users: %v", err)
	}
	if deleted != 6 {
		t.Fatalf("deleted rows = %d, want 6 (including legacy token bucket rows inferred from token_usage)", deleted)
	}

	allowedDaily, err := s.getTokenDailyUsage("tok-allowed-legacy", 2)
	if err != nil {
		t.Fatalf("get allowed daily token usage: %v", err)
	}
	if len(allowedDaily) != 1 || allowedDaily[0].TokenID != "tok-allowed-legacy" {
		t.Fatalf("allowed legacy daily token aggregate was not retained: %+v", allowedDaily)
	}
	allowedHourly, err := s.getTokenHourlyUsage("tok-allowed-legacy", 2)
	if err != nil {
		t.Fatalf("get allowed hourly token usage: %v", err)
	}
	if len(allowedHourly) != 1 || allowedHourly[0].TokenID != "tok-allowed-legacy" {
		t.Fatalf("allowed legacy hourly token aggregate was not retained: %+v", allowedHourly)
	}

	disallowedDaily, err := s.getTokenDailyUsage("tok-disallowed-legacy", 2)
	if err != nil {
		t.Fatalf("get disallowed daily token usage: %v", err)
	}
	if len(disallowedDaily) != 0 {
		t.Fatalf("disallowed legacy daily token aggregate retained: %+v", disallowedDaily)
	}
	disallowedHourly, err := s.getTokenHourlyUsage("tok-disallowed-legacy", 2)
	if err != nil {
		t.Fatalf("get disallowed hourly token usage: %v", err)
	}
	if len(disallowedHourly) != 0 {
		t.Fatalf("disallowed legacy hourly token aggregate retained: %+v", disallowedHourly)
	}
}

func removeJSONFieldFromBucketValue(t *testing.T, s *usageStore, bucketName, key, field string) {
	t.Helper()
	err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return fmt.Errorf("bucket %s missing", bucketName)
		}
		raw := bucket.Get([]byte(key))
		if raw == nil {
			return fmt.Errorf("key %s missing from bucket %s", key, bucketName)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		delete(value, field)
		enc, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), enc)
	})
	if err != nil {
		t.Fatalf("remove JSON field %s from %s/%s: %v", field, bucketName, key, err)
	}
}
