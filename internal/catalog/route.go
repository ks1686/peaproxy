package catalog

import "gopkg.in/yaml.v3"

// Route is a client-facing model name. Target is the live model id it calls.
// Account, when set, pins the route to that one provider account; when empty
// the route fans out to every account that lists Target, in failover order.
//
// The short YAML form `name: target-id` and the long form
// `name: {target: id, account: id}` describe the same route; only the long form
// can pin an account.
type Route struct {
	Target  string `yaml:"target" json:"target"`
	Account string `yaml:"account,omitempty" json:"account,omitempty"`
}

// UnmarshalYAML accepts the short scalar form as well as the mapping form, so a
// config written before account pinning keeps loading unchanged.
func (r *Route) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		r.Target = s
		return nil
	}
	type plain Route
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*r = Route(p)
	return nil
}

// MarshalYAML writes the short form when no account is pinned, so saving a
// config never reshapes a route that did not ask for one.
func (r Route) MarshalYAML() (any, error) {
	if r.Account == "" {
		return r.Target, nil
	}
	type plain Route
	return plain(r), nil
}
