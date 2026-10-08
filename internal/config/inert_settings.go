package config

// InertSettings reports config settings that this version reads, accepts and
// validates but does not act on.
//
// It exists because a setting that silently does nothing is the same failure
// class as a misspelled key: the user believes they changed something and did
// not. A misspelling is already reported (UnknownKeys). This covers the harder
// case, where the spelling is right and the behaviour is still absent.
//
// The rule for adding one here is that the key must be a documented setting
// whose documentation is true. Either it does what the docs say, or it lands
// here and the docs are corrected, or it is removed. A setting listed here is
// reported by `config validate` and refused by `--strict-config`, which makes
// the whole class visible without breaking a config that has carried one for
// months: `validate` still passes and `serve` still starts.
var inertSettings = []struct {
	// Path is the dotted YAML path, as it appears in the config file.
	Path string
	// What it would do, and what happens instead.
	Detail string
}{
	{
		Path: "optimization.persistentContext",
		Detail: "stored context artifacts are memory-only in this release; " +
			"this setting does not write them to disk",
	},
}

// InertSetting is one reported inert setting.
type InertSetting struct {
	Path   string
	Detail string
}

// InertSettings lists the inert settings a loaded config actually sets.
//
// Only settings present in the config are returned. The default file leaves
// them unset, and a config that never mentions one is not warned about: the
// point is to catch someone who believes they turned a feature on.
func (c Config) InertSettings() []InertSetting {
	var out []InertSetting
	if c.Optimization.PersistentContext != nil && *c.Optimization.PersistentContext {
		for _, s := range inertSettings {
			if s.Path == "optimization.persistentContext" {
				out = append(out, InertSetting{Path: s.Path, Detail: s.Detail})
			}
		}
	}
	return out
}
