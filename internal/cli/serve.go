package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/server"
)

func runServe(out io.Writer, cfg config.Config) error {
	_, _ = fmt.Fprintf(out, "starting peaproxy on http://%s\n", cfg.Addr())
	reg := adapters.DefaultRegistry()
	rt := &router.Router{Policy: router.PolicyFillFirst}
	for _, p := range cfg.Providers {
		adp, err := reg.Open(p.Adapter, adapter.Options{
			ID:      p.ID,
			BaseURL: p.BaseURL,
			APIKey:  os.Getenv(p.APIKeyEnv),
			Tier:    catalog.Tier(p.Tier),
		})
		if err != nil {
			_, _ = fmt.Fprintf(out, "skip provider %s: %v\n", p.ID, err)
			continue
		}
		rt.Candidates = append(rt.Candidates, router.Candidate{AccountID: p.ID, Adapter: adp})
	}
	srv := server.New(server.Options{
		Config:   cfg,
		Registry: reg,
		Router:   rt,
	})
	return srv.ListenAndServe()
}
