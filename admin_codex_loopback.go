package main

import (
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var codexOAuthLoopbackCallback = struct {
	sync.Mutex
	servers   []*http.Server
	listeners []net.Listener
}{}

var ensureCodexLoopbackCallbackServersForOperator = ensureCodexLoopbackCallbackServers

func hasPendingAutoCodexSessions() bool {
	codexOAuthSessions.RLock()
	defer codexOAuthSessions.RUnlock()
	for _, session := range codexOAuthSessions.sessions {
		if session != nil && session.AutoComplete {
			return true
		}
	}
	return false
}

func ensureCodexLoopbackCallbackServers(h *proxyHandler) error {
	codexOAuthLoopbackCallback.Lock()
	if len(codexOAuthLoopbackCallback.listeners) > 0 {
		codexOAuthLoopbackCallback.Unlock()
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", h.handleCodexLoopbackCallback)

	var (
		listeners []net.Listener
		servers   []*http.Server
		failures  []string
	)
	for _, addr := range []string{"127.0.0.1:1455", "[::1]:1455"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", addr, err))
			continue
		}
		srv := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       time.Minute,
		}
		listeners = append(listeners, ln)
		servers = append(servers, srv)
	}

	if len(listeners) == 0 {
		codexOAuthLoopbackCallback.Unlock()
		return fmt.Errorf("unable to bind localhost:1455 for Codex OAuth callback (%s)", strings.Join(failures, "; "))
	}

	codexOAuthLoopbackCallback.listeners = listeners
	codexOAuthLoopbackCallback.servers = servers
	codexOAuthLoopbackCallback.Unlock()

	for idx, ln := range listeners {
		srv := servers[idx]
		go func(listener net.Listener, server *http.Server) {
			log.Printf("codex oauth loopback callback listening on http://%s/auth/callback", listener.Addr().String())
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				log.Printf("warning: codex oauth loopback callback server stopped: %v", err)
			}
		}(ln, srv)
	}
	return nil
}

func stopCodexLoopbackCallbackServersIfIdle() {
	if hasPendingAutoCodexSessions() {
		return
	}

	codexOAuthLoopbackCallback.Lock()
	servers := codexOAuthLoopbackCallback.servers
	listeners := codexOAuthLoopbackCallback.listeners
	codexOAuthLoopbackCallback.servers = nil
	codexOAuthLoopbackCallback.listeners = nil
	codexOAuthLoopbackCallback.Unlock()

	for _, srv := range servers {
		_ = srv.Close()
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

func scheduleStopCodexLoopbackCallbackServersIfIdle() {
	go func() {
		time.Sleep(250 * time.Millisecond)
		stopCodexLoopbackCallbackServersIfIdle()
	}()
}

func (h *proxyHandler) handleCodexLoopbackCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		http.Error(w, "loopback access required", http.StatusForbidden)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	oauthErr := strings.TrimSpace(r.URL.Query().Get("error"))
	stateMatch := false
	if state != "" {
		_, _, stateMatch = findCodexSessionByState(state)
	}
	log.Printf(
		"codex oauth loopback callback received: has_code=%t has_state=%t has_error=%t state_match=%t",
		code != "",
		state != "",
		oauthErr != "",
		stateMatch,
	)

	if oauthErr != "" {
		if state != "" {
			if verifier, _, ok := findCodexSessionByState(state); ok {
				finalizeCodexSession(verifier)
			}
		}
		renderCodexLoopbackResult(w, false, "", false, "Codex OAuth was cancelled", oauthErr)
		return
	}

	callbackURL := CodexOAuthRedirectURI
	if strings.TrimSpace(r.URL.RawQuery) != "" {
		callbackURL += "?" + r.URL.RawQuery
	}

	result, err := h.completeCodexExchange(r.Context(), codexExchangeRequest{
		State:       state,
		CallbackURL: callbackURL,
		Lane:        "loopback",
	})
	if err != nil {
		log.Printf("codex oauth loopback callback failed: %v", err)
		renderCodexLoopbackResult(w, false, "", false, "Failed to add Codex account", err.Error())
		return
	}

	title := "Codex account added"
	detail := "You can close this tab and return to codex-pool."
	if result.RefreshedExisting {
		title = "Codex account refreshed"
		detail = "This OAuth flow refreshed an existing Codex seat in place. The total account count may stay the same."
	}
	log.Printf(
		"codex oauth loopback callback completed: account_id=%s refreshed_existing=%t",
		result.AccountID,
		result.RefreshedExisting,
	)
	renderCodexLoopbackResult(w, true, result.AccountID, result.RefreshedExisting, title, detail)
}

func renderCodexLoopbackResult(w http.ResponseWriter, success bool, accountID string, refreshedExisting bool, title, detail string) {
	statusCode := http.StatusOK
	accent := "#3fb950"
	closeScript := `<script>setTimeout(function(){ window.close(); }, 1600);</script>`
	if !success {
		statusCode = http.StatusBadRequest
		accent = "#f85149"
		closeScript = ""
	}

	accountLine := ""
	if strings.TrimSpace(accountID) != "" {
		accountLine = fmt.Sprintf(`<p style="margin: 0 0 10px; color: #c9d1d9;">Account: <code style="background: #21262d; padding: 2px 6px; border-radius: 4px;">%s</code></p>`, html.EscapeString(accountID))
	}
	notifyScript := fmt.Sprintf(`<script>
	(function() {
	  try {
	    if (window.opener && !window.opener.closed) {
	      window.opener.postMessage({
	        type: 'codex-oauth-result',
	        success: %t,
	        account_id: %q,
	        refreshed_existing: %t,
	        title: %q,
	        detail: %q
	      }, '*');
	    }
	  } catch (error) {
	    // Ignore opener messaging failures.
	  }
	})();
	</script>`,
		success,
		accountID,
		refreshedExisting,
		title,
		detail,
	)

	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #0d1117; color: #c9d1d9; margin: 0; padding: 24px; }
    .card { max-width: 720px; margin: 48px auto; background: #161b22; border: 1px solid #30363d; border-radius: 10px; padding: 24px; box-shadow: 0 16px 40px rgba(0,0,0,0.28); }
    h1 { margin: 0 0 12px; color: %s; }
    p { line-height: 1.6; color: #8b949e; }
    a { color: #58a6ff; }
    code { color: #e6edf3; }
  </style>
</head>
<body>
	  <div class="card">
	    <h1>%s</h1>
	    %s
	    <p>%s</p>
	    <p><a href="/status">Return to pool status</a></p>
	  </div>
	  %s
	  %s
	</body>
</html>`,
		html.EscapeString(title),
		accent,
		html.EscapeString(title),
		accountLine,
		html.EscapeString(detail),
		notifyScript,
		closeScript,
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(page))
}
