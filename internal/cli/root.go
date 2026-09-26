package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

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
	root.AddCommand(statusCmd(&configPath))
	root.AddCommand(configCmd(&configPath))
	root.AddCommand(clientsCmd())
	return root
}

func loadCfg(path string) (config.Config, string, error) {
	return config.LoadOrDefault(path)
}

func serveCmd(configPath *string) *cobra.Command {
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
			return runServe(cmd.OutOrStdout(), cfg, path, created)
		},
	}
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
	cmd.AddCommand(list)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "path: %s\nschemaVersion: %d\nbind: %s\nport: %d\nloopback: %v\nrequestLog: %v\nproviders: %d\ncatalog.pin: %d\ncatalog.rename: %d\nhide.blockRouting: %v\nsecrets: %s\n",
				path, cfg.SchemaVersion, cfg.Bind, cfg.Port, config.IsLoopback(cfg.Bind), cfg.RequestLog, len(cfg.Providers), len(cfg.Catalog.Pin), len(cfg.Catalog.Rename), cfg.Hide.BlockRouting, backend)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Exit non-zero if config is invalid",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
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
			store, _ := config.OpenStore(path)
			backend := "file"
			if store != nil {
				backend = string(store.Backend())
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ok\npath: %s\nbind: %s\nloopback: %v\nrequestLog: %v\ncatalog.pin: %d\ncatalog.rename: %d\nproviders: %d\nsecrets: %s\n",
				path, cfg.Addr(), config.IsLoopback(cfg.Bind), cfg.RequestLog, len(cfg.Catalog.Pin), len(cfg.Catalog.Rename), len(cfg.Providers), backend)
			return nil
		},
	})
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
	cmd.AddCommand(&cobra.Command{
		Use:   "show [name]",
		Short: "Print a copy-ready preset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := clients.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown client %q\n  peaproxy clients list", args[0])
			}
			_, _ = io.WriteString(cmd.OutOrStdout(), clients.Format(p))
			return nil
		},
	})
	var doChat bool
	var origin string
	verify := &cobra.Command{
		Use:   "verify [name]",
		Short: "GET /v1/models (and optionally a tiny chat) on the local gateway",
		Args:  cobra.ExactArgs(1),
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
	verify.Flags().StringVar(&origin, "origin", "http://127.0.0.1:8317", "Gateway origin")
	cmd.AddCommand(verify)
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
