package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
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
		Short: "Subscription OAuth login",
		Long: `Login with a consumer subscription (Claude, Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse, GitHub Copilot).

` + oauth.LiabilityWarning() + `

Examples:
  peaproxy auth login --provider anthropic
  peaproxy auth login --provider openai
  peaproxy auth login --provider openai --device
  peaproxy auth login --provider gemini
  peaproxy auth login --provider xai
  peaproxy auth login --provider kimi
  peaproxy auth login --provider meta
  peaproxy auth login --provider copilot
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
		Short: "Start subscription OAuth (ToS/ban risk)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			if provider == "" {
				return fmt.Errorf("missing --provider\n  peaproxy auth login --provider anthropic\n  peaproxy auth login --provider openai\n  peaproxy auth login --provider copilot")
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), oauth.LiabilityWarning())
			adapterName, err := adapters.OAuthAdapterName(provider)
			if err != nil {
				return err
			}
			if accountID == "" {
				accountID = adapters.DefaultOAuthAccountID(adapterName)
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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Waiting for OAuth callback or device approval (5m). Codex also supports --device.")
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Saved %s account %q to %s (email=%s). Tokens are stored in the OS keychain or an encrypted file next to the config, never printed.\n", adapterName, accountID, path, ct.Email)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Smoke: peaproxy models list --filter subscription_oauth")
			return nil
		},
	}
	login.Flags().StringVar(&provider, "provider", "", "anthropic | openai | gemini | xai | kimi | kimi-ai | meta | qwen | copilot | factory | opencode-go")
	login.Flags().StringVar(&accountID, "id", "", "Account id to write (default per provider)")
	login.Flags().BoolVar(&device, "device", false, "Codex device-code flow (xAI/Kimi/Meta already use device code)")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not open a browser")
	login.Flags().BoolVar(&printURL, "print-url", false, "Print the authorize URL and exit without waiting")
	cmd.AddCommand(login)
	return cmd
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
