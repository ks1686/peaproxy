package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
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
	// The count is what /v1/models will actually serve, so it has to come from
	// the same place: Listed excludes hidden rows and unavailable placeholders,
	// which Models does not (#53). A count of 11 next to a list of 7 is worse
	// than no count.
	_, _ = fmt.Fprintf(out, "catalog: %d models (live ListModels; hide is listing-only)\n", len(gw.Listed(catalog.FilterAll)))
	srv := server.New(server.Options{Gateway: gw, ClientRoot: os.Getenv("PEAPROXY_CLIENT_ROOT")})
	return srv.ListenAndServe()
}
