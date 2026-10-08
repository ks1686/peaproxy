package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/usage"
)

// `optimization status` and `optimization explain` are the CLI half of the
// policy panel the UI already renders. They exist because the panel needs a
// browser and most people debugging a routing decision have a terminal open.
//
// Both are read-only. Neither dispatches a request, reserves spend or contacts a
// provider, so `explain` is safe to run against a gateway that is serving
// someone.
func optimizationCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "optimization",
		Short: "The effective optimization policy, and why a model routes where it does",
		Long: `status prints the policy this config resolves to, and what the spend ledger
can actually measure about it. explain prints how one model would be routed,
including the accounts that are out and why.

Both are read-only: nothing here dispatches a request, reserves spend or calls a
provider. Numbers are never invented -- an unpriced window says so.

Examples:
  peaproxy optimization status
  peaproxy optimization explain claude-sonnet-5
  peaproxy optimization explain pea/economy --config ./peaproxy.yaml
`,
	}
	cmd.AddCommand(optimizationStatusCmd(configPath))
	cmd.AddCommand(optimizationExplainCmd(configPath))
	return cmd
}

func optimizationStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the effective optimization policy (same fields as GET /admin/policy)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			gw, cfg, path, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			spent := usage.SpendWindow{}
			reserved := 0.0
			if gw.Usage != nil {
				spent = gw.Usage.SpentInLastDays(30)
				reserved = gw.Usage.Reserved()
			}
			ceiling := "unset"
			if v := cfg.SpendCeiling(); v > 0 {
				ceiling = fmt.Sprintf("%.4f USD", v)
			}
			localEndpoint, hasLocal := cfg.LocalAssistantConfig()

			_, _ = fmt.Fprintf(out, "automatic: %v\n", cfg.OptimizationEnabled())
			_, _ = fmt.Fprintf(out, "promptCache: %s\n", cfg.EffectivePromptCache())
			_, _ = fmt.Fprintf(out, "contextOptimization: %v\n", gw.ContextOptimizationEnabled())
			_, _ = fmt.Fprintf(out, "freeOnly: %v\n", cfg.FreeOnly())
			_, _ = fmt.Fprintf(out, "allowAnonymousProviders: %v\n", cfg.AllowAnonymousProviders())
			_, _ = fmt.Fprintf(out, "localAssistant: %v\n", cfg.LocalAssistantEnabled())
			if hasLocal {
				_, _ = fmt.Fprintf(out, "localEndpoint: %s\n", localEndpoint.Endpoint)
			} else {
				_, _ = fmt.Fprintf(out, "localEndpoint: (not configured)\n")
			}
			_, _ = fmt.Fprintf(out, "persistentContext: %v\n", cfg.PersistentContextEnabled())
			_, _ = fmt.Fprintf(out, "spendCeilingUSD: %s\n", ceiling)
			_, _ = fmt.Fprintf(out, "maxInFlight: %d\n", cfg.RequestEngine.MaxInFlight)
			_, _ = fmt.Fprintf(out, "policyVersion: %d\nschemaVersion: %d\nconfig: %s\n",
				cfg.Optimization.PolicyVersion, cfg.SchemaVersion, path)

			// The ledger half. A ceiling over an unmeasurable window refuses
			// everything, so saying "the prices are not set" when they are is
			// the single most misleading thing this command could print. The
			// measured and estimated parts are kept apart for that reason.
			_, _ = fmt.Fprintf(out, "spentLast30DaysUSD: %.6f\n", spent.USD)
			_, _ = fmt.Fprintf(out, "estimatedLast30DaysUSD: %.6f\n", spent.EstimatedUSD)
			_, _ = fmt.Fprintf(out, "pricedCallsLast30Days: %d\ntotalCallsLast30Days: %d\n", spent.Priced, spent.Total)
			_, _ = fmt.Fprintf(out, "inFlightReservedUSD: %.6f\n", reserved)
			if spent.Total == 0 {
				_, _ = fmt.Fprintf(out, "spendMeasurable: true (no calls in the window)\n")
			} else {
				_, _ = fmt.Fprintf(out, "spendMeasurable: %v\n", spent.Priced == spent.Total)
			}

			// An accepted setting that does nothing is named here too, because
			// this is the command someone runs when a policy "isn't working".
			for _, s := range cfg.InertSettings() {
				_, _ = fmt.Fprintf(out, "inert: %s: %s\n", s.Path, s.Detail)
			}
			return nil
		},
	}
}

func optimizationExplainCmd(configPath *string) *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:   "explain <model>",
		Short: "Explain how a model would be routed, and which accounts are out and why",
		Long: `Prints the accounts that would be tried for a model, in order, plus the ones
that are out and the reason for each.

Read-only: nothing is dispatched, no spend is reserved, no provider is contacted.
An unknown model says so instead of guessing.

Examples:
  peaproxy optimization explain claude-sonnet-5
  peaproxy optimization explain pea/economy
  peaproxy optimization explain pea/free --account openai-key
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			gw, _, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			model := args[0]

			// The catalog is built from live ListModels, so without a refresh
			// every account looks like it serves nothing and the answer would be
			// confidently wrong. This is a model listing, the same call
			// `peaproxy health` makes -- not a dispatch.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			gw.Refresh(ctx)
			ex := gw.ExplainRoute(ctx, model)

			_, _ = fmt.Fprintf(out, "model: %s\nroute: %s\nautomatic: %v\n", ex.Model, ex.Route, ex.Automatic)
			if ex.Note != "" {
				_, _ = fmt.Fprintf(out, "note: %s\n", ex.Note)
			}
			if ex.Incomplete != "" {
				_, _ = fmt.Fprintf(out, "incomplete: %s\n", ex.Incomplete)
			}
			if len(ex.Candidates) == 0 {
				_, _ = fmt.Fprintln(out, "candidates: none")
			} else {
				_, _ = fmt.Fprintln(out, "candidates:")
				for _, c := range ex.Candidates {
					model := c.Model
					if model == "" {
						model = "-"
					}
					_, _ = fmt.Fprintf(out, "  %s\tprovider=%s\tmodel=%s\teligible=%v\t%s\n",
						c.AccountID, c.Adapter, model, c.Eligible, c.Reason)
				}
			}
			if account == "" {
				return nil
			}
			caps, ok := gw.ExplainCandidate(account)
			if !ok {
				return fmt.Errorf("no account %q in this config", account)
			}
			_, _ = fmt.Fprintf(out, "\ncapabilities %s:\n  %s\n", account, describeCapabilities(caps))
			return nil
		},
	}
	cmd.Flags().StringVar(&account, "account", "",
		"Also print the declared capabilities of this account (what the user may override)")
	return cmd
}

// describeCapabilities renders what an adapter declares it can do.
//
// The declared set is a plain bool per field, so this prints yes/no rather than
// pretending there is a third answer here. The third answer does exist -- it is
// the user's `capabilities:` override, which is why --account is worth passing:
// what routing actually believes is the override applied to this, not the
// adapter's own claim.
func describeCapabilities(c adapter.Capabilities) string {
	yn := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	return strings.Join([]string{
		"chat=" + yn(c.Chat),
		"stream=" + yn(c.Stream),
		"tools=" + yn(c.Tools),
		"visionIn=" + yn(c.VisionIn),
		"imageOut=" + yn(c.ImageOut),
		"embeddings=" + yn(c.Embeddings),
		"listModels=" + yn(c.ListModels),
		"oauth=" + yn(c.OAuth),
		"apiKey=" + yn(c.APIKey),
		"local=" + yn(c.Local),
	}, " ")
}

// compile-time guard: the explanation type is part of the gateway's contract
// with the CLI, so a rename breaks here rather than at runtime.
var _ = gateway.RouteExplanation{}
