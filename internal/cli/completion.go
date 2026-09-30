package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// completionCmd is the completion command, written out rather than left to
// cobra's default so that a shell it cannot generate for is an error naming the
// ones it can, instead of help text and a zero exit.
func completionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate a shell completion script for peaproxy",
		Long: `Writes a completion script to stdout. Load it from your shell's startup file.

  bash:  peaproxy completion bash > /etc/bash_completion.d/peaproxy
         (or ~/.local/share/bash-completion/completions/peaproxy)
  zsh:   peaproxy completion zsh > "${fpath[1]}/_peaproxy"
  fish:  peaproxy completion fish > ~/.config/fish/completions/peaproxy.fish

Command and flag names come from the command tree. Client and account preset
names, and the model and provider ids already in your config and usage file,
are completed too, and that part reads nothing but local files: pressing tab
never makes a network call and never opens a keychain prompt.

  peaproxy clients sho<TAB>          clients show/verify/connect/disconnect
  peaproxy accounts add ja<TAB>      accounts add
  peaproxy catalog pin cla<TAB>      catalog pin/rename/hide
  peaproxy catalog hide --kind prov<TAB>
`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unsupported shell %q: use bash, zsh, fish or powershell", args[0])
			}
			return cmd.Help()
		},
	}
	shells := []struct {
		name, short string
		gen         func(*cobra.Command) error
	}{
		{"bash", "Generate the bash completion script", func(c *cobra.Command) error {
			return c.Root().GenBashCompletionV2(c.OutOrStdout(), true)
		}},
		{"zsh", "Generate the zsh completion script", func(c *cobra.Command) error {
			return c.Root().GenZshCompletion(c.OutOrStdout())
		}},
		{"fish", "Generate the fish completion script", func(c *cobra.Command) error {
			return c.Root().GenFishCompletion(c.OutOrStdout(), true)
		}},
		{"powershell", "Generate the PowerShell completion script", func(c *cobra.Command) error {
			return c.Root().GenPowerShellCompletionWithDesc(c.OutOrStdout())
		}},
	}
	for _, sh := range shells {
		shell := sh
		sub := &cobra.Command{
			Use:   shell.name,
			Short: shell.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return shell.gen(cmd)
			},
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

// completeClientNames offers the client presets.
func completeClientNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	names := clients.List()
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeAccountPresets offers the account templates accounts add accepts.
func completeAccountPresets(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	ids := make([]string, 0, 16)
	for _, p := range adapters.AccountPresets() {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeModelID offers the model ids the user already works with: the ones
// pinned, renamed or hidden in the config, plus the ones in the usage file
// beside it. The live catalog is deliberately not consulted -- a tab that
// lists models over the network is worse than no tab completion.
func completeModelID(configPath *string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		ids := knownIDs(*configPath, false)
		sort.Strings(ids)
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeModelOrProviderID is for catalog hide, which takes either depending
// on --kind.
func completeModelOrProviderID(configPath *string, kind func() string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		ids := knownIDs(*configPath, strings.EqualFold(kind(), "provider"))
		sort.Strings(ids)
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

// completionConfig is the part of peaproxy.yaml that completion reads. It is
// read straight off disk rather than through config.Load, which hydrates
// secrets: a keychain prompt in the middle of a tab key, or an error from a
// config the user is halfway through editing, is not something to put in
// someone's terminal.
type completionConfig struct {
	Providers []struct {
		ID string `yaml:"id"`
	} `yaml:"providers"`
	Hide struct {
		Providers []string `yaml:"providers"`
		Models    []string `yaml:"models"`
	} `yaml:"hide"`
	Catalog struct {
		Pin    []string          `yaml:"pin"`
		Rename map[string]string `yaml:"rename"`
	} `yaml:"catalog"`
}

// knownIDs returns the providers or model ids this install already knows about,
// sorted and deduplicated. A missing or unreadable config simply contributes
// nothing: completion must never fail.
func knownIDs(configPath string, provider bool) []string {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	seen := map[string]bool{}
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" {
				seen[id] = true
			}
		}
	}
	var light completionConfig
	if raw, err := os.ReadFile(configPath); err == nil {
		// A malformed file is not an error here; it just offers nothing.
		_ = yaml.Unmarshal(raw, &light)
	}
	if provider {
		add(light.Hide.Providers...)
		for _, p := range light.Providers {
			add(p.ID)
		}
	} else {
		add(light.Hide.Models...)
		add(light.Catalog.Pin...)
		for id := range light.Catalog.Rename {
			add(id)
		}
		if store := usage.Open(filepath.Join(filepath.Dir(configPath), "usage.json")); store != nil {
			for _, e := range store.Recent() {
				add(e.Model)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// completeFreeform keeps a later positional argument out of the model list, for
// commands like catalog rename whose second argument is a display name.
func completeFreeform(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}
