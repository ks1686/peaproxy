package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func catalogCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Listing overlays (pin, rename, hide) matching the Catalog UI",
		Long: `Pin/rename/hide write the same YAML as POST /admin/catalog/overlay and POST /admin/hide.
Live ListModels remains the source of IDs. Hide is listing-only unless hide.blockRouting is true.

Examples:
  peaproxy catalog pin llama3.2
  peaproxy catalog pin llama3.2 --off
  peaproxy catalog rename llama3.2 "Llama 3.2 local"
  peaproxy catalog rename llama3.2 --clear
  peaproxy catalog hide llama3.2
  peaproxy catalog hide llama3.2 --off
  peaproxy catalog hide jan --kind provider
`,
	}

	var pinOff bool
	pin := &cobra.Command{
		Use:               "pin [model-id]",
		Short:             "Pin a live model id to the top of /v1/models (listing only)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeModelID(configPath),
		RunE: func(cmd *cobra.Command, args []string) error {
			gw, _, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			pinned := !pinOff
			if err := gw.SetCatalogOverlay(args[0], nil, &pinned); err != nil {
				return err
			}
			action := "pinned"
			if pinOff {
				action = "unpinned"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", action, args[0])
			return nil
		},
	}
	pin.Flags().BoolVar(&pinOff, "off", false, "Remove the pin")

	var clearRename bool
	rename := &cobra.Command{
		Use:   "rename [model-id] [display-name]",
		Short: "Overlay a display name without changing the routing id",
		Args:  cobra.RangeArgs(1, 2),
		// The second argument is a free-form display name, not a model id.
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) >= 1 {
				return completeFreeform(cmd, args, toComplete)
			}
			return completeModelID(configPath)(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if clearRename {
				name = ""
			} else if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("missing display name\n  peaproxy catalog rename llama3.2 \"Llama 3.2 local\"\n  peaproxy catalog rename llama3.2 --clear")
			} else {
				name = args[1]
			}
			gw, _, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			if err := gw.SetCatalogOverlay(args[0], &name, nil); err != nil {
				return err
			}
			if name == "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cleared rename %s\n", args[0])
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "renamed %s -> %s\n", args[0], name)
			}
			return nil
		},
	}
	rename.Flags().BoolVar(&clearRename, "clear", false, "Remove the display-name overlay")

	var hideOff bool
	var hideKind string
	hide := &cobra.Command{
		Use:   "hide [id]",
		Short: "Hide a model (default) or provider from /v1/models (listing only)",
		Args:  cobra.ExactArgs(1),
		// --kind decides which ids make sense here.
		ValidArgsFunction: completeModelOrProviderID(configPath, func() string { return hideKind }),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := hideKind
			if kind == "" {
				kind = "model"
			}
			switch kind {
			case "model", "provider":
			default:
				return fmt.Errorf("kind must be provider or model")
			}
			gw, _, _, err := openGateway(*configPath)
			if err != nil {
				return err
			}
			if err := gw.ToggleHide(kind, args[0], !hideOff); err != nil {
				return err
			}
			action := "hidden"
			if hideOff {
				action = "unhidden"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s kind=%s (listing only)\n", action, args[0], kind)
			return nil
		},
	}
	hide.Flags().BoolVar(&hideOff, "off", false, "Unhide")
	hide.Flags().StringVar(&hideKind, "kind", "model", "model | provider")

	cmd.AddCommand(pin)
	cmd.AddCommand(rename)
	cmd.AddCommand(hide)
	return cmd
}
