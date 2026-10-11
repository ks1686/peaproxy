package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ks1686/peaproxy/internal/usage"
)

// parseSince accepts a duration like 24h or 7d, a bare number of hours, or a
// date. A window is the one thing everyone gets wrong when reading an error
// log by hand, so the common forms are spelled out rather than requiring the
// caller to know Go's duration syntax.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-24 * time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	d := strings.TrimSuffix(strings.TrimSuffix(s, "d"), "h")
	if n, err := strconv.Atoi(d); err == nil {
		if strings.HasSuffix(s, "d") {
			return now.AddDate(0, 0, -n), nil
		}
		return now.Add(-time.Duration(n) * time.Hour), nil
	}
	if dur, err := time.ParseDuration(s); err == nil {
		return now.Add(-dur), nil
	}
	return time.Time{}, fmt.Errorf("cannot read --since %q: use 24h, 7d, or 2026-10-11", s)
}

type errorBundle struct {
	Generated string             `json:"generated"`
	Since     time.Time          `json:"since"`
	Store     string             `json:"store"`
	Summary   usage.ErrorSummary `json:"summary"`
	Events    []usage.Event      `json:"events"`
}

func errorsCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "errors",
		Short: "Inspect and export recorded failures",
		Long: `Failures are recorded to errors.jsonl next to the config, kept for 7 days.

The store holds one line per request that was not a clean success, with no
prompt previews and with provider bodies redacted, so it is safe to hand to
someone else. usage.json keeps 200 events and requests.log keeps the last 80
after rotation, so both evict failures by traffic rather than by age; this one
is the durable record.

Client disconnects are retained but counted apart from failures: on a busy
machine most recorded errors are an agent closing a stream, and a count that
cannot tell those apart is a count nobody reads.

Examples:
  peaproxy errors summary
  peaproxy errors list --since 24h
  peaproxy errors export --since 7d --out errors.json
`,
	}

	var since string
	var out string

	summary := &cobra.Command{
		Use:   "summary",
		Short: "Count recorded failures by class and by account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			recs, _, err := readErrorRecordsFor(*configPath, since)
			if err != nil {
				return err
			}
			s := usage.SummarizeErrors(recs)
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "failures: %d\ncancels:  %d\n", s.Failures, s.Cancels)
			if len(s.ByClass) > 0 {
				_, _ = fmt.Fprintln(w, "\nby class:")
				for _, c := range sortedKeys(s.ByClass) {
					_, _ = fmt.Fprintf(w, "  %-22s %d\n", c, s.ByClass[c])
				}
			}
			if len(s.ByAcct) > 0 {
				_, _ = fmt.Fprintln(w, "\nby account:")
				for _, a := range sortedKeys(s.ByAcct) {
					_, _ = fmt.Fprintf(w, "  %-22s %d\n", a, s.ByAcct[a])
				}
			}
			return nil
		},
	}
	summary.Flags().StringVar(&since, "since", "24h", "Window: 24h, 7d, or a date")

	list := &cobra.Command{
		Use:   "list",
		Short: "Print recorded failures, newest last",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			recs, path, err := readErrorRecordsFor(*configPath, since)
			if err != nil {
				return err
			}
			if len(recs) == 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no failures recorded in %s (%s)\n", since, path)
				return nil
			}
			for _, e := range recs {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %-20s %-16s %3d  %s\n",
					e.Time.Format(time.RFC3339), e.Class, e.AccountID, e.Status, e.Error)
			}
			return nil
		},
	}
	list.Flags().StringVar(&since, "since", "24h", "Window: 24h, 7d, or a date")

	export := &cobra.Command{
		Use:   "export",
		Short: "Write a redacted bundle for analysis",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			cutoff, err := parseSince(since, now)
			if err != nil {
				return err
			}
			recs, path, err := readErrorRecordsFor(*configPath, since)
			if err != nil {
				return err
			}
			var kept []usage.Event
			for _, e := range recs {
				if !e.Time.Before(cutoff) {
					// Re-redact on the way out. The store redacts on the way
					// in, but a record written by an older build, or edited by
					// hand, must not become the thing that leaks.
					e.Error = usage.Redact(e.Error)
					e.Preview = ""
					kept = append(kept, e)
				}
			}
			b := errorBundle{
				Generated: now.UTC().Format(time.RFC3339),
				Since:     cutoff.UTC(),
				Store:     path,
				Summary:   usage.SummarizeErrors(kept),
				Events:    kept,
			}
			outJSON, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				return err
			}
			if out == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(outJSON))
				return nil
			}
			if err := os.WriteFile(out, append(outJSON, '\n'), 0o600); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d records (%d failures, %d cancels) to %s\n",
				len(kept), b.Summary.Failures, b.Summary.Cancels, out)
			return nil
		},
	}
	export.Flags().StringVar(&since, "since", "24h", "Window: 24h, 7d, or a date")
	export.Flags().StringVar(&out, "out", "", "Write to this file instead of stdout")

	for _, sub := range []*cobra.Command{summary, list, export} {
		cmd.AddCommand(sub)
	}
	return cmd
}

func readErrorRecordsFor(configPath, since string) ([]usage.Event, string, error) {
	cfg, path, err := loadCfg(configPath)
	if err != nil {
		return nil, "", err
	}
	_ = cfg
	store := usage.ErrorLogPath(filepath.Dir(path))
	cutoff, err := parseSince(since, time.Now())
	if err != nil {
		return nil, store, err
	}
	recs, err := usage.ReadErrorRecords(store)
	if err != nil {
		return nil, store, err
	}
	var out []usage.Event
	for _, e := range recs {
		if !e.Time.Before(cutoff) {
			out = append(out, e)
		}
	}
	return out, store, nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
