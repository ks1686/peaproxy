package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func requestsCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "Opt-in redacted request inspector",
		Long: `Tails requests.log when requestLog is on (same inspector as GET /admin/requests).

Examples:
  peaproxy requests tail
  peaproxy requests tail --n 20 --config ./peaproxy.yaml
`,
	}
	var n int
	tail := &cobra.Command{
		Use:   "tail",
		Short: "Print newest redacted request-log events (requires requestLog)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			gw, cfg, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			if !cfg.RequestLog {
				return fmt.Errorf("request log is off; set requestLog: true, PEAPROXY_REQUEST_LOG=1, or the UI toggle (docs/CONFIG.md)")
			}
			path := ""
			if gw.Usage != nil {
				path = gw.Usage.RequestLogPath()
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "requestLog: true\npath: %s\n", path)
			if gw.Usage == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "events: none")
				return nil
			}
			events := gw.Usage.Tail(n)
			if len(events) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "events: none")
				return nil
			}
			for _, e := range events {
				ts := ""
				if !e.Time.IsZero() {
					ts = e.Time.UTC().Format(time.RFC3339) + "\t"
				}
				errBit := ""
				if e.Error != "" {
					errBit = "\t" + e.Error
				}
				quotaBit := ""
				if e.QuotaHint != "" {
					quotaBit = "\t" + e.QuotaHint
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s%s\t%s\t%s\t%d%s%s\t%s\n",
					ts, e.AccountID, e.Model, e.Protocol, e.Status, errBit, quotaBit, e.Preview)
			}
			return nil
		},
	}
	tail.Flags().IntVar(&n, "n", 100, "Max events, newest first (same default as GET /admin/requests)")
	cmd.AddCommand(tail)
	return cmd
}
