package config

import "reflect"

// Merge reconciles mine (a writer's in-memory config) with disk (what another
// process may have written since) against base (what mine looked like when it
// was last loaded or saved). It never aliases its inputs.
//
// It knows nothing about which of disk's secrets were unreadable, so it never
// carries mine's copy of one forward; SaveMerged, which loads disk itself, calls
// merge with that set. Callers that have not loaded the store should use this
// form.
//
// Providers merge per id: an account mine changed (or added) keeps mine's copy;
// an account mine left untouched takes disk's copy, so a re-login or token
// refresh done elsewhere survives; an account removed on one side and untouched
// on the other is removed; accounts only on disk and not in base were added
// elsewhere and are appended in disk order. Disk's copy of an untouched
// account keeps mine's secret only where disk's copy was unreadable (see
// keepSecrets); a secret the user deleted stays deleted.
//
// Every other field is mine's value if mine changed it from base, else disk's.
//
// Hide, Expose, Catalog, Failover, RequestEngine, AutomaticRoutes and Routes are
// each taken as a whole struct, not merged per element, so two sides editing
// different entries of the same struct do lose one side's edit. A union would
// be worse: an entry removed on purpose is indistinguishable from one never
// set, so nothing could ever be un-hidden or un-routed again. The CLI and the
// server avoid the collision by the server adopting these from disk on its next
// save and from watching the file, not by merging finer.
func Merge(base, disk, mine Config) Config {
	return merge(base, disk, mine, nil)
}

// merge is Merge with the set of secrets that could not be read back from disk.
func merge(base, disk, mine Config, unreadable unreadableSecrets) Config {
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
	out.Optimization = pick(base.Optimization, disk.Optimization, mine.Optimization)
	out.Routes = pick(base.Routes, disk.Routes, mine.Routes)
	out.Providers = mergeProviders(base.Providers, disk.Providers, mine.Providers, unreadable)
	return Clone(out)
}

func pick[T any](base, disk, mine T) T {
	if !reflect.DeepEqual(mine, base) {
		return mine
	}
	return disk
}

func mergeProviders(base, disk, mine []Provider, unreadable unreadableSecrets) []Provider {
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
			out = append(out, keepSecrets(d, m, unreadable))
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

// keepSecrets fills disk's unreadable secrets from mine, and only those. Loading
// disk treats an unreadable stored secret as absent, and adopting that copy
// would drop a live token until the next load, so a corrupt or undecryptable
// secret is worth rescuing from memory.
//
// A secret that is merely absent is not: no `oauth:` block, an entry the user
// deleted, or an account switched to apiKeyEnv all mean removed on purpose, and
// carrying mine's copy forward would write the secret straight back (#54). The
// secrets are APIKey and, as a set, the OAuth AccessToken, RefreshToken,
// IDToken and secret Extra keys, taken with the ExpiresAt that belongs to them;
// disk's identity fields (Email, AccountID, PlanType, public Extra) are kept.
func keepSecrets(disk, mine Provider, unreadable unreadableSecrets) Provider {
	if disk.APIKey == "" && unreadable.has(secretRef{disk.ID, secretAPIKey}) {
		disk.APIKey = mine.APIKey
	}
	if oauthHasSecret(disk.OAuth) || !oauthHasSecret(mine.OAuth) {
		return disk
	}
	if !unreadable.has(secretRef{disk.ID, secretOAuth}) {
		return disk
	}
	var tok OAuthToken
	if disk.OAuth != nil {
		tok = *disk.OAuth
	}
	tok.AccessToken, tok.RefreshToken, tok.IDToken = mine.OAuth.AccessToken, mine.OAuth.RefreshToken, mine.OAuth.IDToken
	tok.ExpiresAt = mine.OAuth.ExpiresAt
	extra := make(map[string]string, len(tok.Extra)+len(mine.OAuth.Extra))
	for k, v := range tok.Extra {
		extra[k] = v
	}
	for k, v := range mine.OAuth.Extra {
		if extraKeyIsSecret(k) {
			extra[k] = v
		}
	}
	tok.Extra = nil
	if len(extra) > 0 {
		tok.Extra = extra
	}
	disk.OAuth = &tok
	return disk
}
