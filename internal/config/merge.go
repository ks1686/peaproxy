package config

import "reflect"

// Merge reconciles mine (a writer's in-memory config) with disk (what another
// process may have written since) against base (what mine looked like when it
// was last loaded or saved). It never aliases its inputs.
//
// Providers merge per id: an account mine changed (or added) keeps mine's copy;
// an account mine left untouched takes disk's copy, so a re-login or token
// refresh done elsewhere survives; an account removed on one side and untouched
// on the other is removed; accounts only on disk and not in base were added
// elsewhere and are appended in disk order.
//
// Every other field is mine's value if mine changed it from base, else disk's.
func Merge(base, disk, mine Config) Config {
	var out Config
	out.SchemaVersion = pick(base.SchemaVersion, disk.SchemaVersion, mine.SchemaVersion)
	out.Bind = pick(base.Bind, disk.Bind, mine.Bind)
	out.Port = pick(base.Port, disk.Port, mine.Port)
	out.AdminToken = pick(base.AdminToken, disk.AdminToken, mine.AdminToken)
	out.AllowNonLoopback = pick(base.AllowNonLoopback, disk.AllowNonLoopback, mine.AllowNonLoopback)
	out.RequestLog = pick(base.RequestLog, disk.RequestLog, mine.RequestLog)
	out.Hide = pick(base.Hide, disk.Hide, mine.Hide)
	out.Expose = pick(base.Expose, disk.Expose, mine.Expose)
	out.Catalog = pick(base.Catalog, disk.Catalog, mine.Catalog)
	out.Failover = pick(base.Failover, disk.Failover, mine.Failover)
	out.RequestEngine = pick(base.RequestEngine, disk.RequestEngine, mine.RequestEngine)
	out.AutomaticRoutes = pick(base.AutomaticRoutes, disk.AutomaticRoutes, mine.AutomaticRoutes)
	out.Routes = pick(base.Routes, disk.Routes, mine.Routes)
	out.Providers = mergeProviders(base.Providers, disk.Providers, mine.Providers)
	return Clone(out)
}

func pick[T any](base, disk, mine T) T {
	if !reflect.DeepEqual(mine, base) {
		return mine
	}
	return disk
}

func mergeProviders(base, disk, mine []Provider) []Provider {
	byID := func(ps []Provider) map[string]Provider {
		m := make(map[string]Provider, len(ps))
		for _, p := range ps {
			m[p.ID] = p
		}
		return m
	}
	inBase, inDisk, inMine := byID(base), byID(disk), byID(mine)
	var out []Provider
	for _, m := range mine {
		b, wasBase := inBase[m.ID]
		changed := !wasBase || !reflect.DeepEqual(m, b)
		d, onDisk := inDisk[m.ID]
		switch {
		case onDisk && !changed:
			out = append(out, d)
		case onDisk || changed:
			out = append(out, m)
		}
		// Untouched here and gone from disk: removed elsewhere.
	}
	for _, d := range disk {
		if _, ok := inMine[d.ID]; ok {
			continue
		}
		if _, ok := inBase[d.ID]; ok {
			continue // removed here
		}
		out = append(out, d)
	}
	return out
}
