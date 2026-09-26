package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/version"
	"github.com/spf13/cobra"
)

func healthCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Adapter health, quota remaining, cooldowns, and bind flags matching GET /admin/health",
		Long: `Prints the same fields as GET /admin/health (no admin token on loopback).
Refreshes live ListModels so adapterHealth, quota, and models match a running serve.
Quota remaining is omitted when the provider does not report it (never invented as 0).

Examples:
  peaproxy health
  peaproxy health --config ./peaproxy.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			gw, cfg, path, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			gw.Refresh(ctx)
			usagePath := ""
			if gw.Usage != nil {
				usagePath = gw.Usage.Path()
			}
			lan := cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind)
			adminRequired := lan
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status: ok\nversion: %s\nbind: %s\nport: %d\noauth: subscription OAuth (ToS risk)\nconfig: %s\nusageFile: %s\nrequestLog: %v\nmodels: %d\nadapters: %s\nfailoverPolicy: %s\n",
				version.Version, cfg.Bind, cfg.Port, path, usagePath, cfg.RequestLog, len(gw.Models()), strings.Join(adapters.Names(), ", "), cfg.FailoverPolicy())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "adapterHealth:")
			health := gw.AdapterHealth()
			if len(health) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
			}
			for _, h := range health {
				errBit := ""
				if h.Error != "" {
					errBit = "\terror=" + h.Error
				}
				checked := ""
				if !h.CheckedAt.IsZero() {
					checked = "\tcheckedAt=" + h.CheckedAt.UTC().Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\tadapter=%s\tstatus=%s\tmodels=%d\tlatencyMs=%d%s%s\n",
					h.AccountID, h.Adapter, h.Status, h.Models, h.LatencyMS, checked, errBit)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "quota:")
			qs := gw.Quota()
			if len(qs) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
			}
			for _, q := range qs {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\tadapter=%s\tsource=%s\t%s\n",
					q.AccountID, q.Adapter, q.Source, q.Format())
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "cooldowns:")
			cds := gw.Cooldowns()
			if len(cds) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
			}
			for _, c := range cds {
				quotaBit := ""
				if c.QuotaHint != "" {
					quotaBit = "\tquota=" + c.QuotaHint
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\treason=%s\tremainingMs=%d\tuntil=%s%s\n",
					c.AccountID, c.Reason, c.RemainingMs, c.Until.UTC().Format(time.RFC3339), quotaBit)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cooldownTtlMs: %d\nallowNonLoopback: %v\nlan: %v\nadminTokenRequired: %v\n",
				gateway.CooldownTTL.Milliseconds(), cfg.AllowNonLoopback, lan, adminRequired)
			return nil
		},
	}
}
