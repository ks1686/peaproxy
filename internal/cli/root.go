package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
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

OAuth adapters in this tree are stubs. Do not expect working Claude/ChatGPT login yet.

Examples:
  peaproxy serve
  peaproxy serve --bind 127.0.0.1 --port 8317
  peaproxy models list --filter free
  peaproxy clients show cursor
`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to peaproxy.yaml (optional)")

	root.AddCommand(serveCmd(&configPath))
	root.AddCommand(authCmd())
	root.AddCommand(accountsCmd(&configPath))
	root.AddCommand(modelsCmd(&configPath))
	root.AddCommand(statusCmd(&configPath))
	root.AddCommand(configCmd(&configPath))
	root.AddCommand(clientsCmd())
	return root
}

func loadCfg(path string) (config.Config, error) {
	if path == "" {
		return config.Default(), nil
	}
	return config.Load(path)
}

func serveCmd(configPath *string) *cobra.Command {
	bind := config.DefaultBind
	port := config.DefaultPort
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the localhost gateway and UI",
		Long: `Bind defaults to 127.0.0.1:8317. Non-loopback bind requires config
allowNonLoopback plus an admin token.

Examples:
  peaproxy serve
  peaproxy serve --port 8317
  peaproxy serve --config ./peaproxy.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("bind") {
				cfg.Bind = bind
			}
			if cmd.Flags().Changed("port") {
				cfg.Port = port
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			return runServe(cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", config.DefaultBind, "Listen address (loopback default)")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "Listen port")
	return cmd
}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Subscription OAuth login (stub)",
		Long: `Examples:
  peaproxy auth login --provider anthropic
  peaproxy auth login --provider openai
`,
	}
	login := &cobra.Command{
		Use:   "login",
		Short: "Start an OAuth login (not implemented)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			provider, _ := cmd.Flags().GetString("provider")
			if provider == "" {
				return fmt.Errorf("missing --provider\n  peaproxy auth login --provider anthropic\n  known stubs: anthropic, openai")
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "auth login: provider=%s status=not-implemented\nTODO: spike OAuth without reverse-engineering private clients\n", provider)
			return nil
		},
	}
	login.Flags().String("provider", "", "Provider stub: anthropic | openai")
	cmd.AddCommand(login)
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
		Short: "Print account stubs from config",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			if len(cfg.Providers) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "accounts: none")
				return nil
			}
			for _, p := range cfg.Providers {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\tadapter=%s\ttier=%s\n", p.ID, p.Adapter, p.Tier)
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
		Short: "Inspect the live catalog (stub list until serve is up)",
		Long: `Examples:
  peaproxy models list
  peaproxy models list --filter free
  peaproxy models list --filter local
`,
	}
	var filter string
	list := &cobra.Command{
		Use:   "list",
		Short: "List models after hide/filter (empty until adapters are queried)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "filter=%s hide.providers=%v hide.models=%v\n", filter, cfg.Hide.Providers, cfg.Hide.Models)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "models: (none in CLI stub — start peaproxy serve and GET /v1/models)")
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
		Short: "Show bind address and scaffold phase",
		Long: `Examples:
  peaproxy status
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status: scaffold\nlisten: %s\noauth: not implemented\nui: http://%s/\n", cfg.Addr(), cfg.Addr())
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
		Short: "Print the config path flag (empty means defaults)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			p := *configPath
			if p == "" {
				p = "(defaults; pass --config)"
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), p)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print effective config as YAML-ish text",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "schemaVersion: %d\nbind: %s\nport: %d\nproviders: %d\n", cfg.SchemaVersion, cfg.Bind, cfg.Port, len(cfg.Providers))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Exit non-zero if config is invalid",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			cfg, err := loadCfg(*configPath)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	})
	return cmd
}

func clientsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "Harness presets (Cursor, Claude Code, OpenCode, Pi, Codex, Continue)",
		Long: `Examples:
  peaproxy clients list
  peaproxy clients show cursor
  peaproxy clients verify cursor
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
	cmd.AddCommand(&cobra.Command{
		Use:   "verify [name]",
		Short: "Smoke-check a harness preset (stub)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := clients.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown client %q\n  peaproxy clients list", args[0])
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "verify %s: not implemented\n%s\n", p.Name, p.VerifyTODO)
			return nil
		},
	})
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
