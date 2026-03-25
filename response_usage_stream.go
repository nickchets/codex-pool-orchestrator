package main

import (
	"io"
	"log"
	"sync"
	"time"
)

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
