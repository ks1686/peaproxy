package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/version"
	"github.com/spf13/cobra"
)

// NewRoot builds the peaproxy command tree. Flags are non-interactive (agents first).
func NewRoot() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:   "peaproxy",
		Short: "Local multi-provider AI gateway",
		Long: `PeaProxy is a localhost gateway: subscription OAuth + API keys + free/local
providers, a live model catalog, and OpenAI/Claude-compatible endpoints.

Subscription OAuth (Claude, Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse) is
implemented and may violate provider ToS — see docs/OAUTH.md. API keys remain
the official path.

Examples:
  peaproxy serve
  peaproxy serve --bind 127.0.0.1 --port 8317
  peaproxy models list --filter free
  peaproxy catalog pin llama3.2
  peaproxy accounts add jan-local
  peaproxy health
  peaproxy requests tail
  peaproxy clients show cursor
  peaproxy clients show opencode
  peaproxy clients show claude-code
`,
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("peaproxy {{.Version}}\n")
	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to peaproxy.yaml (default: user config dir)")

	root.AddCommand(serveCmd(&configPath))
	root.AddCommand(authCmd(&configPath))
	root.AddCommand(accountsCmd(&configPath))
	root.AddCommand(modelsCmd(&configPath))
	root.AddCommand(catalogCmd(&configPath))
	root.AddCommand(requestsCmd(&configPath))
	root.AddCommand(healthCmd(&configPath))
	root.AddCommand(statusCmd(&configPath))
	root.AddCommand(optimizationCmd(&configPath))
	root.AddCommand(configCmd(&configPath))
	root.AddCommand(clientsCmd())
	// Replace cobra's default completion command: the scripts are the same, but
	// an unsupported shell becomes an error that names the supported ones.
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(completionCmd())
	return root
}

func openGateway(configPath string) (*gateway.Gateway, config.Config, string, error) {
	cfg, path, err := loadCfg(configPath)
	if err != nil {
		return nil, config.Config{}, "", err
	}
	gw, err := gateway.New(cfg, path, adapters.DefaultRegistry())
	if err != nil {
		return nil, cfg, path, err
	}
	return gw, cfg, path, nil
}

func loadCfg(path string) (config.Config, string, error) {
	return config.LoadOrDefault(path)
}

func serveCmd(configPath *string) *cobra.Command {
	var strictConfig bool
	bind := config.DefaultBind
	port := config.DefaultPort
	allowLAN := false
	adminToken := ""
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the localhost gateway and UI",
		Long: `Bind defaults to 127.0.0.1:8317. Binding 0.0.0.0 requires --allow-lan
plus a non-empty admin token (--admin-token or PEAPROXY_ADMIN_TOKEN).

A missing config file is written on first serve (Default() skeleton).

Examples:
  peaproxy serve
  peaproxy serve --port 8317
  peaproxy serve --bind 0.0.0.0 --allow-lan --admin-token "$TOKEN"
  peaproxy serve --config ./peaproxy.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			// Before EnsureFile, so a typo is named before the load that
			// validates can fail on it.
			if err := reportConfigTypos(cmd.ErrOrStderr(), *configPath, strictConfig); err != nil {
				return err
			}
			cfg, path, created, err := config.EnsureFile(*configPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("bind") {
				cfg.Bind = bind
			}
			if cmd.Flags().Changed("port") {
				cfg.Port = port
			}
			if cmd.Flags().Changed("allow-lan") && allowLAN {
				cfg.AllowNonLoopback = true
			}
			if cmd.Flags().Changed("admin-token") {
				cfg.AdminToken = adminToken
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.ValidateKnownAdapters(adapters.Names()); err != nil {
				return err
			}
			if err := reportInertSettings(cmd.ErrOrStderr(), cfg, strictConfig); err != nil {
				return err
			}
			return runServe(cmd.OutOrStdout(), cfg, path, created)
		},
	}
	cmd.Flags().BoolVar(&strictConfig, "strict-config", false,
		"Refuse to start when the config carries keys this version does not read "+
			"(default: warn and ignore)")
	cmd.Flags().StringVar(&bind, "bind", config.DefaultBind, "Listen address (loopback default)")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "Listen port")
	cmd.Flags().BoolVar(&allowLAN, "allow-lan", false, "Permit non-loopback bind (also requires --admin-token)")
	cmd.Flags().StringVar(&adminToken, "admin-token", "", "Admin token required for /admin when bound off loopback")
	return cmd
}

func accountsCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "List configured provider accounts",
		Long: `Examples:
  peaproxy accounts list
  peaproxy accounts list --config ./peaproxy.yaml
  peaproxy accounts add jan-local
  peaproxy accounts add gpt4all-local
  peaproxy accounts add sambanova-key
  peaproxy accounts add workers-ai --account-id "$CLOUDFLARE_ACCOUNT_ID"
`,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "Print accounts from config",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, _, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			if len(cfg.Providers) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "accounts: none")
				return nil
			}
			for _, p := range cfg.Providers {
				auth := "none"
				switch {
				case p.HasOAuth():
					auth = "oauth"
				case p.APIKeyEnv != "":
					auth = "env:" + p.APIKeyEnv
				case p.APIKey != "":
					auth = "key"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\tadapter=%s\ttier=%s\tbase=%s\tauth=%s\n", p.ID, p.Adapter, p.Tier, p.BaseURL, auth)
			}
			return nil
		},
	}
	var (
		idFlag        string
		accountIDFlag string
		baseURLFlag   string
		apiKeyEnvFlag string
		apiKeyFlag    string
		tierFlag      string
	)
	add := &cobra.Command{
		Use:   "add [preset]",
		Short: "Add an account from a UI preset (Jan, GPT4All, SambaNova, Workers AI, …)",
		Long: `Uses the same templates as GET /admin/presets.

Workers AI replaces YOUR_ACCOUNT_ID from --account-id or CLOUDFLARE_ACCOUNT_ID.
The env value is never printed.

Examples:
  peaproxy accounts add jan-local
  peaproxy accounts add gpt4all-local
  peaproxy accounts add sambanova-key
  peaproxy accounts add workers-ai --account-id <cloudflare-account-id>
`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAccountPresets,
		RunE: func(cmd *cobra.Command, args []string) error {
			prov, err := providerFromPreset(args[0], idFlag, baseURLFlag, accountIDFlag, apiKeyFlag, apiKeyEnvFlag, tierFlag)
			if err != nil {
				return err
			}
			if w := presetWarn(args[0]); w != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), w)
			}
			gw, _, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := gw.AddProvider(ctx, prov); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added %s\tadapter=%s\ttier=%s\tbase=%s\n", prov.ID, prov.Adapter, prov.Tier, prov.BaseURL)
			return nil
		},
	}
	add.Flags().StringVar(&idFlag, "id", "", "Account id (default: preset id)")
	add.Flags().StringVar(&accountIDFlag, "account-id", "", "Fills Workers AI YOUR_ACCOUNT_ID (or set CLOUDFLARE_ACCOUNT_ID)")
	add.Flags().StringVar(&baseURLFlag, "base-url", "", "Override preset base URL")
	add.Flags().StringVar(&apiKeyEnvFlag, "api-key-env", "", "Env var holding the API key (default: preset envKey)")
	add.Flags().StringVar(&apiKeyFlag, "api-key", "", "Inline API key (prefer --api-key-env; stored in the secret store)")
	add.Flags().StringVar(&tierFlag, "tier", "", "Tier override (local|free|freemium|paid)")
	cmd.AddCommand(list)
	cmd.AddCommand(add)
	return cmd
}

func modelsCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Live catalog from configured adapters",
		Long: `Examples:
  peaproxy models list
  peaproxy models list --filter free
  peaproxy models list --filter local
`,
	}
	var filter string
	list := &cobra.Command{
		Use:   "list",
		Short: "List models after listing-only hide/filter",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, path, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			gw, err := gateway.New(cfg, path, adapters.DefaultRegistry())
			if err != nil {
				return err
			}
			gw.Refresh(context.Background())
			listed := gw.Listed(catalog.Filter(filter))
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "filter=%s hide.providers=%v hide.models=%v listed=%d (hidden still routable)\n",
				filter, cfg.Hide.Providers, cfg.Hide.Models, len(listed))
			for _, m := range listed {
				name := m.ID
				if m.DisplayName != "" && m.DisplayName != m.ID {
					name = m.DisplayName + " (" + m.ID + ")"
				}
				pin := ""
				if m.Pinned {
					pin = "\tpin"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\ttier=%s\tprovider=%s\taccount=%s%s\n", name, m.Tier, m.Provider, m.AccountID, pin)
			}
			return nil
		},
	}
	list.Flags().StringVar(&filter, "filter", "all", "all | free | paid | local | subscription_oauth")
	cmd.AddCommand(list)
	return cmd
}

func statusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bind address and phase",
		Long: `Examples:
  peaproxy status
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, path, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status: %s\nlisten: %s\nconfig: %s\nrequestLog: %v\noauth: subscription (ToS risk; docs/OAUTH.md)\nsecrets: OS keychain or encrypted file (docs/CONFIG.md)\nui: http://%s/\n", version.Version, cfg.Addr(), path, cfg.RequestLog, cfg.Addr())
			return nil
		},
	}
}

func configCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or validate YAML config",
		Long: `Examples:
  peaproxy config path
  peaproxy config show
  peaproxy config validate --config ./peaproxy.yaml
`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the resolved config path",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			_, path, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print effective config summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, path, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			store, _ := config.OpenStore(path)
			backend := "file"
			if store != nil {
				backend = string(store.Backend())
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "path: %s\nschemaVersion: %d\nbind: %s\nport: %d\nloopback: %v\nrequestLog: %v\nproviders: %d\ncatalog.pin: %d\ncatalog.rename: %d\nhide.blockRouting: %v\nfailover.policy: %s\nsecrets: %s\n",
				path, cfg.SchemaVersion, cfg.Bind, cfg.Port, config.IsLoopback(cfg.Bind), cfg.RequestLog, len(cfg.Providers), len(cfg.Catalog.Pin), len(cfg.Catalog.Rename), cfg.Hide.BlockRouting, cfg.FailoverPolicy(), backend)
			return nil
		},
	})
	var validateStrict bool
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Exit non-zero if config is invalid",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			// Ahead of the load, for the reason in reportConfigTypos.
			if err := reportConfigTypos(cmd.ErrOrStderr(), *configPath, validateStrict); err != nil {
				return err
			}
			cfg, path, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.ValidateKnownAdapters(adapters.Names()); err != nil {
				return err
			}
			if err := reportInertSettings(cmd.ErrOrStderr(), cfg, validateStrict); err != nil {
				return err
			}
			// Validate has to reach the verdict serve reaches. Reporting "ok"
			// for a config that refuses to start is worse than saying nothing:
			// it is a false assurance from the one command whose entire job is
			// to catch this before the gateway does.
			if err := checkAdaptersBuild(cfg, cmd.ErrOrStderr()); err != nil {
				return err
			}
			store, _ := config.OpenStore(path)
			backend := "file"
			if store != nil {
				backend = string(store.Backend())
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ok\npath: %s\nbind: %s\nloopback: %v\nrequestLog: %v\ncatalog.pin: %d\ncatalog.rename: %d\nfailover.policy: %s\nproviders: %d\nsecrets: %s\n",
				path, cfg.Addr(), config.IsLoopback(cfg.Bind), cfg.RequestLog, len(cfg.Catalog.Pin), len(cfg.Catalog.Rename), cfg.FailoverPolicy(), len(cfg.Providers), backend)
			return nil
		},
	}
	validateCmd.Flags().BoolVar(&validateStrict, "strict-config", false,
		"Fail when the config carries keys this version does not read (default: warn and continue)")
	cmd.AddCommand(validateCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write the default config file if missing",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, path, created, err := config.EnsureFile(*configPath)
			if err != nil {
				return err
			}
			if created {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d providers)\n", path, len(cfg.Providers))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "exists %s (%d providers)\n", path, len(cfg.Providers))
			}
			return nil
		},
	})
	return cmd
}

func clientsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "Harness presets (Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, Amp)",
		Long: `Examples:
  peaproxy clients list
  peaproxy clients show cursor
  peaproxy clients show opencode
  peaproxy clients show claude-code
  peaproxy clients show pi
  peaproxy clients show continue
  peaproxy clients show cline
  peaproxy clients show amp
  peaproxy clients verify cursor
  peaproxy clients verify cursor --chat
`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List preset names",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			for _, n := range clients.List() {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return nil
		},
	})
	var showOrigin string
	show := &cobra.Command{
		Use:               "show [name]",
		Short:             "Print a copy-ready preset",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClientNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := clients.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown client %q\n  peaproxy clients list", args[0])
			}
			_, _ = io.WriteString(cmd.OutOrStdout(), clients.Format(clients.WithOrigin(p, showOrigin)))
			return nil
		},
	}
	show.Flags().StringVar(&showOrigin, "origin", clients.DefaultOrigin, "Gateway origin to print (a trailing /v1 is accepted)")
	cmd.AddCommand(show)
	var doChat bool
	var origin string
	verify := &cobra.Command{
		Use:               "verify [name]",
		Short:             "GET /v1/models (and optionally a tiny chat) on the local gateway",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClientNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			res, err := clients.Verify(ctx, args[0], origin, doChat)
			_, _ = io.WriteString(cmd.OutOrStdout(), clients.FormatVerify(res))
			if res.Detail != "" && err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.Detail)
			}
			return err
		},
	}
	verify.Flags().BoolVar(&doChat, "chat", false, "Also POST a tiny completion using the first listed model")
	verify.Flags().StringVar(&origin, "origin", clients.DefaultOrigin, "Gateway origin (a trailing /v1 is accepted)")
	cmd.AddCommand(verify)
	var root string
	var importAccount string
	var importOrigin string
	var importAdminToken string
	importCmd := &cobra.Command{
		Use:   "import pi",
		Short: "Add live models to Pi as a PeaProxy-owned account provider",
		Long: "Reads the admin catalog from the gateway and writes routable models for the selected account " +
			"into a PeaProxy-owned Pi provider. Claude and GPT models are skipped because Pi serves them " +
			"with built-in providers. Re-running is idempotent; disconnect removes only the owned provider.",
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{"pi"}, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "pi" {
				return fmt.Errorf("import supports only pi (got %q)", args[0])
			}
			if importAccount == "" {
				return errors.New("--account is required, e.g. --account work")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			token := importAdminToken
			if token == "" {
				token = os.Getenv("PEAPROXY_ADMIN_TOKEN")
			}
			models, err := clients.FetchPiModels(ctx, importOrigin, token)
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()
			lay := clients.Layout{Root: home}
			if root != "" {
				lay = clients.Layout{Root: root}
			}
			count, err := lay.ImportPi(importAccount, importOrigin, models)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "imported %d model(s) into providers.peaproxy-%s\n", count, importAccount)
			return nil
		},
	}
	importCmd.Flags().StringVar(&importAccount, "account", "", "Account id; the provider is named peaproxy-<account>")
	importCmd.Flags().StringVar(&importOrigin, "origin", clients.DefaultOrigin, "Gateway origin (a trailing /v1 is accepted)")
	importCmd.Flags().StringVar(&importAdminToken, "admin-token", "", "Admin API token (defaults to PEAPROXY_ADMIN_TOKEN)")
	cmd.AddCommand(importCmd)
	var model string
	var baseURL string
	layout := func() clients.Layout {
		if root == "" {
			home, _ := os.UserHomeDir()
			return clients.Layout{Root: home}
		}
		return clients.Layout{Root: root}
	}
	detect := &cobra.Command{
		Use:   "detect",
		Short: "List managed harness configs under --root",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			found := layout().Detect()
			if len(found) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "none")
				return nil
			}
			for _, item := range found {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", item.Name, item.Path)
			}
			return nil
		},
	}
	connect := &cobra.Command{
		Use:               "connect [name]",
		Short:             "Add the PeaProxy block to a managed harness config",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClientNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := layout().Connect(args[0], baseURL, model)
			if errors.Is(err, clients.ErrGuidedSetup) {
				if preset, ok := clients.Get(args[0]); ok {
					_, _ = fmt.Fprint(cmd.OutOrStdout(), clients.WithOrigin(preset, baseURL).Snippet)
				}
				return nil
			}
			return err
		},
	}
	disconnect := &cobra.Command{
		Use:               "disconnect [name]",
		Short:             "Remove only the PeaProxy block from a managed harness config",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClientNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return layout().Disconnect(args[0])
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show managed harness files under --root",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "root %s\n", layout().Root)
			for _, item := range layout().Detect() {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", item.Name, item.Path)
			}
			return nil
		},
	}
	for _, sub := range []*cobra.Command{detect, connect, disconnect, status} {
		sub.Flags().StringVar(&root, "root", "", "Config directory (default: home). Tests and agents should pass a temp directory.")
		cmd.AddCommand(sub)
	}
	connect.Flags().StringVar(&model, "model", "", "Model id to record")
	connect.Flags().StringVar(&baseURL, "origin", clients.DefaultOrigin, "Gateway origin (a trailing /v1 is accepted; each client gets the suffix its wire needs)")
	return cmd
}

// Execute runs the CLI.
func Execute() error {
	return NewRoot().Execute()
}

// ExecuteWithArgs is used in tests.
func ExecuteWithArgs(args []string, out *bytes.Buffer) error {
	cmd := NewRoot()
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(os.Stdin)
	return cmd.Execute()
}

// checkAdaptersBuild opens every enabled account's adapter, which is the same
// check the gateway performs before it listens. Running the real constructor
// rather than restating its rules is the point: a mirrored rule drifts, and then
// validate once again approves a config that will not start.
//
// It also mirrors what the gateway does about a failure. rebuild collects the
// first error and carries on, and only returns it when no account at all could
// be built -- one account that cannot start takes itself out of service, but it
// does not stop the gateway while another account still works. Returning on the
// first error here would refuse configs serve starts without complaint, which is
// the same false assurance this check was added to remove.
//
// A partial failure is reported as a warning rather than an exit code, because
// the gateway genuinely starts in that state.
func checkAdaptersBuild(cfg config.Config, w io.Writer) error {
	reg := adapters.DefaultRegistry()
	var first error
	built := 0
	for _, p := range cfg.Providers {
		if p.Adapter == "" {
			continue
		}
		// Disabled accounts are never constructed by the gateway, so
		// constructing one here would fail validation for a config serve
		// starts without complaint. The check has to match startup, and
		// startup is the gateway's behaviour, not a stricter one.
		if p.Disabled {
			continue
		}
		if _, err := reg.Open(p.Adapter, adapter.Options{
			ID:        p.ID,
			BaseURL:   p.BaseURL,
			APIKey:    p.ResolveKey(),
			SessionID: p.SessionID,
			Tier:      catalog.Tier(p.Tier),
		}); err != nil {
			if first == nil {
				first = fmt.Errorf("account %q: %w", p.ID, err)
			}
			continue
		}
		built++
	}
	if built == 0 && first != nil {
		return first
	}
	if first != nil && w != nil {
		_, _ = fmt.Fprintf(w, "\nwarning: %v\n  the gateway will start without that account; every model only it\n"+
			"  serves becomes unavailable until it is fixed.\n", first)
	}
	return nil
}

// reportInertSettings names config settings this version reads and accepts but
// does not act on.
//
// The two lists are different problems with the same shape. An unknown key is a
// spelling mistake; an inert setting is spelled correctly and still does
// nothing, which is harder to notice and silently wastes the user's effort.
// Under --strict-config an inert setting is an error, because that mode's whole
// job is answering "does this config still do what it says".
func reportInertSettings(w io.Writer, cfg config.Config, strict bool) error {
	inert := cfg.InertSettings()
	if len(inert) == 0 {
		return nil
	}
	if strict {
		_, _ = fmt.Fprintf(w, "\n%d config setting(s) are accepted but not acted on by this version, and --strict-config is set:\n", len(inert))
		for _, s := range inert {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", s.Path, s.Detail)
		}
		return fmt.Errorf("%d config setting(s) are accepted but not acted on by this version and strict mode is set. "+
			"Remove them, or drop --strict-config to warn instead", len(inert))
	}
	_, _ = fmt.Fprintf(w, "\nwarning: %d config setting(s) are accepted but not acted on by this version:\n", len(inert))
	for _, s := range inert {
		_, _ = fmt.Fprintf(w, "  %s: %s\n", s.Path, s.Detail)
	}
	_, _ = fmt.Fprintf(w, "  the setting is accepted so an existing config keeps loading; it does not do this\n")
	return nil
}

// warnUnknownKeys reports config keys the loader does not read.
//
// It warns rather than refuses on purpose. A config carrying one works today,
// and a typo does not stop it working -- it stops the setting from being what
// the author wrote, which is quieter and worse. Failing here would break a
// running deployment over a spelling, so this names the key, names the key it
// probably meant, and leaves the exit code alone.
func warnUnknownKeys(w io.Writer, path string) error {
	return reportUnknownKeys(w, path, false)
}

// strictUnknownKeys turns the same list into the error it should have been from
// the start, for anyone who has cleaned their config and wants the guarantee.
//
// This is opt-in because refusing is a breaking change: a typo that has been
// sitting in a running config, quietly ignored, would stop that proxy booting.
// That is the right behaviour for a config that has never worked and the wrong
// behaviour for one that has been serving all along, and PeaProxy cannot tell
// them apart from the text alone. So the default stays a warning and the
// guarantee is a flag the operator turns on once their config is clean.
//
// --strict-config also makes `config validate` fail, which is the useful half:
// it turns "does this config still have a typo in it" into a thing a CI job or
// a pre-commit hook can ask.
func strictUnknownKeys(w io.Writer, path string) error {
	return reportUnknownKeys(w, path, true)
}

// reportConfigTypos names unreadable keys before anything validates the file.
//
// It has to run before loading, not after. config.Load runs cfg.Validate() on
// the way out, so a typo whose symptom is a validation failure -- `adaptor` for
// `adapter` -- produced "provider missing adapter" and returned before the
// unknown-key check ran at all. The author was told what broke and never what
// to change. This reads the file directly so the diagnosis comes first.
func reportConfigTypos(w io.Writer, path string, strict bool) error {
	if path == "" {
		path = config.DefaultPath()
	}
	if _, err := os.Stat(path); err != nil {
		return nil // missing, unreadable, or not written yet; the caller reports that
	}
	if strict {
		return strictUnknownKeys(w, path)
	}
	return warnUnknownKeys(w, path)
}

// describeUnknown names one unreadable key, and what it was probably meant to
// be, because the typo is the diagnosis.
func describeUnknown(u config.UnknownKey) string {
	where := u.Path
	if where == "" {
		where = "top level"
	}
	if u.Nearest != "" {
		return fmt.Sprintf("%s.%s -- did you mean %q?", where, u.Key, u.Nearest)
	}
	return fmt.Sprintf("%s.%s", where, u.Key)
}

func reportUnknownKeys(w io.Writer, path string, strict bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // loadCfg already reported a missing or unreadable file
	}
	unknown, err := config.UnknownKeys(raw)
	if err != nil || len(unknown) == 0 {
		return nil
	}
	if strict {
		_, _ = fmt.Fprintf(w, "\n%d config key(s) are not read by this version, and --strict-config is set:\n", len(unknown))
		for _, u := range unknown {
			_, _ = fmt.Fprintf(w, "  %s\n", describeUnknown(u))
		}
		// Deliberately not "refusing to start": `config validate` returns this
		// too, and it starts nothing. The non-zero exit is the refusal there.
		return fmt.Errorf("%d config key(s) are not read by this version and strict mode is set. "+
			"Fix them, or drop --strict-config to warn instead", len(unknown))
	}
	_, _ = fmt.Fprintf(w, "\nwarning: %d config key(s) are not read by this version and are ignored:\n", len(unknown))
	for _, u := range unknown {
		_, _ = fmt.Fprintf(w, "  %s\n", describeUnknown(u))
	}
	_, _ = fmt.Fprintf(w, "  ignored keys keep their default, so the setting stays whatever it was\n")
	return nil
}
