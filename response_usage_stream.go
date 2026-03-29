package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

type claudePingTailCutoffError struct {
	accountID       string
	stalledFor      time.Duration
	timeout         time.Duration
	lastNonPingType string
}

func (e *claudePingTailCutoffError) Error() string {
	if e == nil {
		return "claude ping-only tail cutoff"
	}
	return fmt.Sprintf(
		"claude ping-only tail cutoff account=%s stalled_for=%s timeout=%s last_non_ping_type=%s",
		e.accountID,
		e.stalledFor,
		e.timeout,
		e.lastNonPingType,
	)
}

func matchClaudePingTailCutoff(err error, acc *Account) (*claudePingTailCutoffError, bool) {
	if acc == nil || !isGitLabClaudeAccount(acc) {
		return nil, false
	}
	var cutoff *claudePingTailCutoffError
	if !errors.As(err, &cutoff) || cutoff == nil {
		return nil, false
	}
	if strings.TrimSpace(cutoff.accountID) != "" && cutoff.accountID != acc.ID {
		return nil, false
	}
	return cutoff, true
}

type claudePingTailWatcher struct {
	accountID           string
	trace               *requestTrace
	timeout             time.Duration
	lastNonPingAt       time.Time
	lastNonPingType     string
	sawContentDelta     bool
	sawContentBlockStop bool
	sawMessageStop      bool
}

func newClaudePingTailWatcher(accountID string, trace *requestTrace, timeout time.Duration) *claudePingTailWatcher {
	if timeout <= 0 {
		return nil
	}
	return &claudePingTailWatcher{
		accountID: accountID,
		trace:     trace,
		timeout:   timeout,
	}
}

func (w *claudePingTailWatcher) noteEvent(eventType string) error {
	if w == nil {
		return nil
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return nil
	}
	now := time.Now()
	if eventType == "ping" {
		if !w.sawContentDelta || !w.sawContentBlockStop || w.sawMessageStop || w.lastNonPingAt.IsZero() {
			return nil
		}
		stalledFor := now.Sub(w.lastNonPingAt)
		if stalledFor < w.timeout {
			return nil
		}
		if w.trace != nil {
			w.trace.noteEvent(
				"claude_tail_cutoff",
				"account=%s stalled_ms=%d last_non_ping_type=%q timeout_ms=%d",
				w.accountID,
				stalledFor.Milliseconds(),
				w.lastNonPingType,
				w.timeout.Milliseconds(),
			)
		}
		return &claudePingTailCutoffError{
			accountID:       w.accountID,
			stalledFor:      stalledFor,
			timeout:         w.timeout,
			lastNonPingType: w.lastNonPingType,
		}
	}

	w.lastNonPingAt = now
	w.lastNonPingType = eventType
	switch eventType {
	case "content_block_delta":
		w.sawContentDelta = true
	case "content_block_stop":
		w.sawContentBlockStop = true
	case "message_stop":
		w.sawMessageStop = true
	}
	return nil
}

func (h *proxyHandler) wrapUsageInterceptWriter(
	reqID string,
	writer io.Writer,
	provider Provider,
	acc *Account,
	userID string,
	trace *requestTrace,
	headerPrimaryPct float64,
	headerSecondaryPct float64,
	managedStreamFailed *bool,
	managedStreamFailureOnce *sync.Once,
) io.Writer {
	var claudeAccum *RequestUsage
	var claudeTailWatcher *claudePingTailWatcher
	if acc != nil && acc.Type == AccountTypeClaude && isGitLabClaudeAccount(acc) {
		claudeTailWatcher = newClaudePingTailWatcher(acc.ID, trace, h.cfg.claudePingTailTimeout)
		if trace != nil {
			trace.noteEvent(
				"claude_tail_guard_enabled",
				"account=%s auth_mode=%q timeout_ms=%d",
				acc.ID,
				accountAuthMode(acc),
				h.cfg.claudePingTailTimeout.Milliseconds(),
			)
		}
	}

	return &sseInterceptWriter{
		w: writer,
		eventCallback: func(data []byte) {
			if trace != nil {
				trace.noteSSEEvent(data, false)
			}
			if !isManagedCodexAPIKeyAccount(acc) {
				return
			}
			disposition, ok := classifyManagedOpenAIAPISSEError(data)
			if !ok {
				return
			}
			managedStreamFailureOnce.Do(func() {
				*managedStreamFailed = true
				applyManagedOpenAIAPIDisposition(acc, disposition, nil, time.Now())
				if err := saveAccount(acc); err != nil {
					log.Printf("[%s] warning: failed to save managed api key %s stream failure: %v", reqID, acc.ID, err)
				}
				log.Printf("[%s] managed api key %s stream failure: dead=%v rate_limited=%v reason=%s", reqID, acc.ID, disposition.MarkDead, disposition.RateLimit, disposition.Reason)
			})
		},
		eventHook: func(eventType string, _ []byte) error {
			if claudeTailWatcher == nil {
				return nil
			}
			return claudeTailWatcher.noteEvent(eventType)
		},
		callback: func(data []byte) {
			obj, ok := parseUsageEventObject(data)
			if !ok {
				if h.cfg.debug {
					log.Printf("[%s] SSE callback: failed to parse usage event", reqID)
				}
				return
			}

			var ru *RequestUsage
			if provider.Type() == AccountTypeCodex {
				delta := parseCodexUsageDelta(obj)
				if delta.Snapshot != nil {
					applyUsageSnapshot(acc, delta.Snapshot)
					persistUsageSnapshot(h.store, acc)
				}
				ru = delta.Usage
			} else {
				ru = provider.ParseUsage(obj)
			}
			if ru == nil {
				return
			}
			if trace != nil {
				trace.noteSSEUsageEvent(data)
			}

			if acc.Type == AccountTypeClaude {
				if claudeAccum == nil {
					claudeAccum = ru
					return
				}
				claudeAccum.OutputTokens = ru.OutputTokens
				claudeAccum.BillableTokens = clampNonNegative(
					claudeAccum.InputTokens - claudeAccum.CachedInputTokens + ru.OutputTokens)
				ru = claudeAccum
				claudeAccum = nil
			}

			h.recordUsage(acc, *enrichUsageRecord(acc, userID, ru, headerPrimaryPct, headerSecondaryPct))
		},
	}
}
