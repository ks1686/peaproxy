package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/spf13/cobra"
)

func authCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Subscription OAuth login (Claude / Codex)",
		Long: `Login with a Claude Pro/Max or ChatGPT/Codex subscription.

` + oauth.LiabilityWarning() + `

Examples:
  peaproxy auth login --provider anthropic
  peaproxy auth login --provider openai
  peaproxy auth login --provider openai --device
  peaproxy auth login --provider anthropic --print-url
`,
	}
	var (
		provider  string
		accountID string
		device    bool
		noBrowser bool
		printURL  bool
	)
	login := &cobra.Command{
		Use:   "login",
		Short: "Start Claude or Codex subscription OAuth",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			if provider == "" {
				return fmt.Errorf("missing --provider\n  peaproxy auth login --provider anthropic\n  peaproxy auth login --provider openai")
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), oauth.LiabilityWarning())
			adapterName, err := oauthAdapterName(provider)
			if err != nil {
				return err
			}
			if accountID == "" {
				accountID = defaultOAuthAccountID(adapterName)
			}
			opts := adapter.Options{
				ID:           accountID,
				SkipLoopback: printURL,
			}
			if device {
				opts.OAuthFlow = "device"
			}
			var saved oauth.Token
			opts.PersistOAuth = func(t oauth.Token) error {
				saved = t
				return nil
			}
			adp, err := adapters.DefaultRegistry().Open(adapterName, opts)
			if err != nil {
				return err
			}
			auth, ok := adp.(adapter.Authenticator)
			if !ok {
				return fmt.Errorf("%s does not implement OAuth", adapterName)
			}
			ctx := context.Background()
			sess, err := auth.AuthStart(ctx)
			if err != nil {
				return err
			}
			if sess.UserCode != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Device code: %s\nOpen: %s\n", sess.UserCode, sess.LoginURL)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Open this URL to continue:\n%s\n", sess.LoginURL)
			}
			if printURL {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "print-url: not waiting for callback. Run without --print-url to complete login.")
				return nil
			}
			if !noBrowser && sess.LoginURL != "" {
				if err := openBrowser(sess.LoginURL); err != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Could not open a browser (%v). Open the URL manually.\n", err)
				}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Waiting for OAuth callback (5m). If the browser never returns, paste the redirect URL and re-run, or use --device for Codex.")
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := auth.AuthComplete(waitCtx, sess, ""); err != nil {
				return err
			}
			cfg, path, _, err := config.EnsureFile(*configPath)
			if err != nil {
				return err
			}
			ct := config.OAuthFromRuntime(saved)
			updated := false
			for i := range cfg.Providers {
				if cfg.Providers[i].ID == accountID {
					cfg.Providers[i].Adapter = adapterName
					cfg.Providers[i].OAuth = &ct
					if cfg.Providers[i].Tier == "" {
						cfg.Providers[i].Tier = "paid"
					}
					updated = true
					break
				}
			}
			if !updated {
				cfg.Providers = append(cfg.Providers, config.Provider{
					ID:      accountID,
					Adapter: adapterName,
					Tier:    "paid",
					OAuth:   &ct,
				})
			}
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Saved %s account %q to %s (email=%s). Tokens are never printed.\n", adapterName, accountID, path, ct.Email)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Smoke: peaproxy models list --filter subscription_oauth")
			return nil
		},
	}
	login.Flags().StringVar(&provider, "provider", "", "anthropic | openai")
	login.Flags().StringVar(&accountID, "id", "", "Account id to write (default anthropic-oauth / openai-oauth)")
	login.Flags().BoolVar(&device, "device", false, "Codex device-code flow (no loopback port)")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not open a browser")
	login.Flags().BoolVar(&printURL, "print-url", false, "Print the authorize URL and exit without waiting")
	cmd.AddCommand(login)
	return cmd
}

func oauthAdapterName(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "anthropic_oauth", "claude":
		return "anthropic_oauth", nil
	case "openai", "openai_oauth", "chatgpt", "codex":
		return "openai_oauth", nil
	default:
		return "", fmt.Errorf("unknown OAuth provider %q (anthropic | openai). Gemini/xAI OAuth is not implemented", provider)
	}
}

func defaultOAuthAccountID(adapterName string) string {
	switch adapterName {
	case "anthropic_oauth":
		return "anthropic-oauth"
	default:
		return "openai-oauth"
	}
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
