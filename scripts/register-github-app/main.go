// register-github-app.go — One-shot GitHub App registration via manifest flow.
//
// Usage:
//   go run register-github-app.go [--name NAME] [--webhook-url URL]
//
// 1. Starts local HTTP server on port 21111.
// 2. Prints a URL for the user to open in their browser.
// 3. User clicks "Create GitHub App" on GitHub.
// 4. GitHub redirects to localhost:21111/callback?code=CODE.
// 5. Exchanges code via `gh api` for full credentials (app_id, pem, webhook_secret, etc.).
// 6. Writes credentials to ~/.config/qws941-bot/env and private-key.pem.
// 7. Optionally installs the app on the authenticated account.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

const (
	callbackPort = 21111
	configDir    = ".config/qws941-bot"
)

type manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     map[string]string `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

type conversionResponse struct {
	ID            int    `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func main() {
	appName := flag.String("name", "qws941-bot", "GitHub App name")
	webhookURL := flag.String("webhook-url", "https://bot.jclee.me/webhook", "Webhook URL")
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	cfgDir := filepath.Join(home, configDir)

	// Ensure config directory exists
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: create config dir: %v\n", err)
		os.Exit(1)
	}

	// Build manifest
	m := manifest{
		Name: *appName,
		URL:  "https://bot.jclee.me",
		HookAttributes: map[string]string{
			"url": *webhookURL,
		},
		RedirectURL: fmt.Sprintf("http://localhost:%d/callback", callbackPort),
		Public:      false,
		DefaultPermissions: map[string]string{
			"issues":        "write",
			"pull_requests": "write",
			"metadata":      "read",
		},
		DefaultEvents: []string{
			"issues",
			"issue_comment",
			"pull_request",
			"pull_request_review_comment",
		},
	}

	manifestJSON, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: marshal manifest: %v\n", err)
		os.Exit(1)
	}

	// Channel to receive the code from the callback
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()

	// Landing page with auto-submit form
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Register %s</title></head>
<body>
<h2>Register GitHub App: %s</h2>
<p>Click the button to create the app on GitHub.</p>
<form action="https://github.com/settings/apps/new" method="post">
  <input type="hidden" name="manifest" value='%s'>
  <button type="submit" style="font-size:1.2em;padding:10px 24px;cursor:pointer">
    Create GitHub App
  </button>
</form>
</body>
</html>`, *appName, *appName, strings.ReplaceAll(string(manifestJSON), "'", "&#39;"))
	})

	// Callback handler — GitHub redirects here with ?code=...
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Success</title></head>
<body>
<h2>✓ Code received</h2>
<p>Exchanging code for credentials… check your terminal.</p>
</body>
</html>`)
		codeCh <- code
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", callbackPort),
		Handler: mux,
	}

	// Start server
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("server: %w", err)
		}
	}()

	registrationURL := fmt.Sprintf("http://localhost:%d", callbackPort)

	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  GitHub App Registration — Manifest Flow                    ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Open this URL in your browser:                             ║\n")
	fmt.Printf("║  %s                              ║\n", registrationURL)
	fmt.Println("║                                                              ║")
	fmt.Println("║  Then click 'Create GitHub App' on the page.                ║")
	fmt.Println("║  GitHub will redirect back here automatically.              ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Waiting for GitHub redirect…")

	// Also print direct manifest URL for SSH port-forward scenarios
	encodedManifest := url.QueryEscape(string(manifestJSON))
	directURL := fmt.Sprintf("https://github.com/settings/apps/new?manifest=%s", encodedManifest)
	fmt.Println()
	fmt.Println("Alternative (direct link — paste in browser):")
	fmt.Println(directURL)
	fmt.Println()

	// Wait for code or interrupt
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var code string
	select {
	case code = <-codeCh:
		fmt.Printf("Received code: %s\n", code)
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	case <-ctx.Done():
		fmt.Println("\nInterrupted.")
		srv.Shutdown(context.Background())
		os.Exit(0)
	}

	// Shut down the HTTP server
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)

	// Exchange code for credentials via gh api
	fmt.Println("Exchanging code for credentials…")
	out, err := exec.Command("gh", "api",
		"--method", "POST",
		fmt.Sprintf("/app-manifests/%s/conversions", code),
	).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "gh api stderr: %s\n", exitErr.Stderr)
		}
		fmt.Fprintf(os.Stderr, "fatal: exchange code: %v\n", err)
		os.Exit(1)
	}

	var resp conversionResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: parse response: %v\n", err)
		fmt.Fprintf(os.Stderr, "raw response: %s\n", out)
		os.Exit(1)
	}

	fmt.Printf("App created: id=%d slug=%s owner=%s\n", resp.ID, resp.Slug, resp.Owner.Login)

	// Save PEM
	pemPath := filepath.Join(cfgDir, "private-key.pem")
	if err := os.WriteFile(pemPath, []byte(resp.PEM), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: write PEM: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Private key saved: %s\n", pemPath)

	// Get installation ID — install the app first
	fmt.Println("Fetching installation ID…")
	installID := getInstallationID(resp.ID)

	// Write env file
	envContent := fmt.Sprintf(`# GitHub App credentials — %s
# Auto-generated by register-github-app.go at %s
BOT_WEBHOOK_SECRET=%s
BOT_APP_ID=%d
BOT_INSTALLATION_ID=%s
BOT_PRIVATE_KEY_PATH=%s
BOT_CALLBACK_BASE=http://127.0.0.1:1111
`,
		resp.Slug,
		time.Now().Format(time.RFC3339),
		resp.WebhookSecret,
		resp.ID,
		installID,
		pemPath,
	)

	envPath := filepath.Join(cfgDir, "env")
	if err := os.WriteFile(envPath, []byte(envContent), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: write env: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Env file written: %s\n", envPath)

	// Print summary
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  Registration Complete                                       ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  App ID:          %d\n", resp.ID)
	fmt.Printf("║  Slug:            %s\n", resp.Slug)
	fmt.Printf("║  Client ID:       %s\n", resp.ClientID)
	fmt.Printf("║  Webhook Secret:  %s…\n", resp.WebhookSecret[:16])
	fmt.Printf("║  PEM:             %s\n", pemPath)
	fmt.Printf("║  Env:             %s\n", envPath)
	fmt.Printf("║  Installation ID: %s\n", installID)
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	if installID == "PENDING" {
		fmt.Println("⚠  Installation ID not yet available.")
		fmt.Printf("   Install the app at: https://github.com/settings/apps/%s/installations\n", resp.Slug)
		fmt.Println("   Then update BOT_INSTALLATION_ID in the env file.")
	} else {
		fmt.Println("Next steps:")
		fmt.Println("  systemctl --user restart qws941-bot")
		fmt.Println("  systemctl --user status qws941-bot")
	}
}

// getInstallationID tries to find the installation ID for the app.
// The app must be installed on the account first.
func getInstallationID(appID int) string {
	// Try listing installations for the authenticated user
	out, err := exec.Command("gh", "api", "/user/installations",
		"--jq", fmt.Sprintf(".installations[] | select(.app_id == %d) | .id", appID),
	).Output()
	if err == nil {
		id := strings.TrimSpace(string(out))
		if id != "" {
			return id
		}
	}

	// App might not be installed yet — prompt user
	fmt.Printf("\nApp %d not yet installed. Install it now:\n", appID)
	fmt.Printf("  https://github.com/apps/qws941-bot/installations/new\n")
	fmt.Println("Press Enter after installing…")

	var input string
	fmt.Scanln(&input)

	// Retry
	out, err = exec.Command("gh", "api", "/user/installations",
		"--jq", fmt.Sprintf(".installations[] | select(.app_id == %d) | .id", appID),
	).Output()
	if err == nil {
		id := strings.TrimSpace(string(out))
		if id != "" {
			return id
		}
	}

	return "PENDING"
}
