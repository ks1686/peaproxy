package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/server"
)

func runServe(out io.Writer, cfg config.Config, path string, created bool) error {
	if created {
		_, _ = fmt.Fprintf(out, "wrote first-run config %s\n", path)
	}
	_, _ = fmt.Fprintf(out, "starting peaproxy on http://%s\nconfig: %s\n", cfg.Addr(), path)
	if cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind) {
		_, _ = fmt.Fprintf(out, "WARNING: listening on %s — /admin requires X-Admin-Token\n", cfg.Bind)
	}
	gw, err := gateway.New(cfg, path, adapters.DefaultRegistry())
	if err != nil {
		return err
	}
	gw.Refresh(context.Background())
	_, _ = fmt.Fprintf(out, "catalog: %d models (live ListModels; hide is listing-only)\n", len(gw.Models()))
	srv := server.New(server.Options{Gateway: gw})
	return srv.ListenAndServe()
}
