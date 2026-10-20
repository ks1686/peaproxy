package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/contextopt"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/localruntime"
	"github.com/ks1686/peaproxy/internal/responsecache"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/streamguard"
	"github.com/ks1686/peaproxy/internal/translate"
)

// Chat proxies a non-stream OpenAI chat.completions body with policy-based failover.
func (g *Gateway) Chat(ctx context.Context, raw []byte) (adapter.ChatResponse, Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return adapter.ChatResponse{}, Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.ChatResponse{}, Call{}, err
	}
	// Both response caches are keyed on the caller's bytes. Once context
	// optimization injects this session's artifacts, an identical body from a
	// different session is a different request, so an answer derived from one
	// session must never be served to another -- through the durable cache or the
	// in-flight one. One decision, taken once, guards read, coalesce and store.
	// It is hoisted above both candidate loops because it does not depend on the
	// instance being tried.
	sessionSpecific := g.sessionSpecificBody(raw, session, cands)
	clientOwnsProxyTool := contextopt.DeclaresProxyTool(raw)
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if !sessionSpecific {
			if hit, ok := g.cachedChat(inst.Provider.ID, inst.Provider.BaseURL, cacheModel(model, budget), raw); ok {
				hit.Raw = echoClientModel(hit.Raw, client, model)
				if client != model {
					hit.Model = client
				}
				return hit, Call{Account: inst.Provider.ID, Deployment: model}, nil
			}
		}
	}
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	coalesce := g.live().RequestEngine.CacheResponses && responsecache.Eligible("chat", raw, true)
	for _, inst := range cands {
		if g.notChatModel(inst.Provider.ID, model) {
			// Already refused once. Do not spend an upstream call rediscovering it.
			last = errModelRefusesChat
			continue
		}
		model, raw := inst.applyModel(model, raw)
		var resp adapter.ChatResponse
		var callErr error
		var attempted atomic.Bool
		// callOnce issues one upstream attempt. It is forward-declared so the
		// proxy tool loop below can wrap it without the two closures having to
		// be ordered around each other.
		var callOnce func(context.Context, []byte, bool) (adapter.ChatResponse, error)
		callUpstream := func(runCtx context.Context) (adapter.ChatResponse, error) {
			// Proxy tool rounds wrap the transport retries rather than the other
			// way round: a search is part of answering one request, so a failed
			// re-send is a failed round, not a new request.
			body := raw
			rounds := contextopt.NewBudget()
			first := true
			for {
				resp, err := callOnce(runCtx, body, first)
				if err != nil {
					return resp, err
				}
				calls := contextopt.ProxyToolCalls(resp.Raw)
				if len(calls) == 0 || g.Artifacts == nil || clientOwnsProxyTool {
					return resp, nil
				}
				if !rounds.Take() {
					// The model kept searching past the limit. Returning the
					// round would hand the client a pea_search call it never
					// made and cannot answer, so the turn ends here with the
					// reason instead. A truncated answer presented as a
					// complete one is the same failure wearing a hat.
					//
					// The round was still fetched upstream and still billed, so
					// its usage is recorded before it goes. Dropping it here
					// left the one round the client certainly paid for as the
					// one call the ledger could not see -- and the round that
					// ends a runaway loop is not a round anyone would notice
					// going missing.
					g.recordDiscardedRound(inst.Provider.ID, model, resp.Raw)
					return adapter.ChatResponse{}, RoundLimitError{reason: rounds.Stop()}
				}
				results := contextopt.RunSearches(g.Artifacts, session, calls)
				next := contextopt.AppendToolResults(body, calls, results)
				if string(next) == string(body) {
					// The body could not carry a result. Stopping beats resending
					// an identical request the model will answer the same way.
					return resp, nil
				}
				body = next
				// This round's response is about to be replaced by the next
				// one, so its usage has nowhere else to go. The provider billed
				// it; the ledger has to know it happened.
				g.recordDiscardedRound(inst.Provider.ID, model, resp.Raw)
				first = false
			}
		}
		callOnce = func(runCtx context.Context, body []byte, countAttempt bool) (adapter.ChatResponse, error) {
			var once adapter.ChatResponse
			var onceErr error
			for attempt := 0; attempt < 2; attempt++ {
				// The attempt budget exists to stop a retry storm on failure. A
				// proxy search round is a successful call, so charging it against
				// that budget would stop a legitimate search after one round.
				if countAttempt {
					if err := budgetAttempts.take(); err != nil {
						return adapter.ChatResponse{}, errAttemptBudgetExhausted
					}
				}
				attempted.Store(true)
				once, onceErr = admitted(g, runCtx, inst.Provider.ID, model, g.bodyFor(body, inst, session), func(c context.Context) (adapter.ChatResponse, error) {
					return inst.Adapter.Chat(c, chatReq(inst, model, g.bodyFor(body, inst, session), false, budget))
				})
				if onceErr == nil || !router.Transient(onceErr) || attempt == 1 {
					break
				}
			}
			return once, onceErr
		}
		if coalesce && g.flight != nil && !sessionSpecific {
			var body []byte
			var shared bool
			body, shared, callErr = g.flight.DoShared(ctx, responsecache.Key(inst.Provider.ID, inst.Provider.BaseURL, cacheModel(model, budget), "chat", raw), func(runCtx context.Context) ([]byte, error) {
				once, err := callUpstream(runCtx)
				if err != nil {
					return nil, err
				}
				return once.Raw, nil
			})
			if callErr == nil {
				// A caller that joined another's call made no upstream request;
				// recording the shared usage again would bill it once per caller.
				resp.Raw, resp.CacheHit = body, shared
			}
		} else {
			resp, callErr = callUpstream(ctx)
		}
		if attempted.Load() || (coalesce && !errors.Is(callErr, errAttemptBudgetExhausted)) {
			inst.recordAttempt(&lastAccount)
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			if client != model {
				resp.Model = client
			}
			if !sessionSpecific {
				g.storeChat(lastAccount, inst.Provider.BaseURL, cacheModel(model, budget), raw, resp.Raw)
			}
			return resp, Call{Account: lastAccount, Deployment: model}, nil
		}
		last = callErr
		// Protocol refusal first: the model cannot serve this protocol, which is
		// not the account's fault, and every other account offering the same id
		// is skipped once they refuse (#83). Then admission: busy is not broken,
		// so skip without cooling (#74). Then replay: cooldown and try the next.
		if g.noteAttempt(callErr, inst.Provider.ID, model, &st, failPolicy{Protocol: true}) {
			continue
		}
		return adapter.ChatResponse{}, Call{Account: lastAccount, Deployment: model}, callErr
	}
	if st.Refused {
		return adapter.ChatResponse{}, Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	if st.AdmissionSkipped {
		return adapter.ChatResponse{}, Call{Account: lastAccount, Deployment: model}, last
	}
	return adapter.ChatResponse{}, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
}

// streamPrelude keeps the configured first-event limit, and lengthens it only
// while a local model is still loading. The request deadline is unchanged.
func (g *Gateway) streamPrelude(model string) time.Duration {
	base := g.live().StreamPreludeTimeout()
	const coldStart = 30 * time.Second
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, m := range g.models {
		if m.ID == model && m.Tier == catalog.TierLocal && m.Status == string(localruntime.StateLoading) && base < coldStart {
			return coldStart
		}
	}
	return base
}

func (g *Gateway) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, g.live().RequestDeadline())
}

// ChatStream proxies SSE with failover before any bytes are written.
func (g *Gateway) ChatStream(ctx context.Context, raw []byte, w io.Writer) (Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return Call{}, err
	}
	cw := &countWriter{w: w}
	guard := streamguard.New(cw, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if g.notChatModel(inst.Provider.ID, model) {
			last = errModelRefusesChat
			st.Refused = true
			continue
		}
		dest := newRouteRewriter(guard, model, client)
		var callErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := budgetAttempts.take(); err != nil {
				return Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			guard.Reset()
			if budgetAttempts.final(i, len(cands)-1) {
				guard.Unbounded()
			}
			before := cw.n
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			_, callErr = admitted(g, attemptCtx, inst.Provider.ID, model, g.bodyFor(raw, inst, session), func(c context.Context) (struct{}, error) {
				// Drain any held partial token before the guard finishes, so the
				// bytes reach the client in the order they arrived.
				err := inst.Adapter.ChatStream(c, chatReq(inst, model, g.bodyFor(raw, inst, session), true, budget), dest)
				return struct{}{}, noteStream(guard, flushAfter(dest, err))
			})
			stop()
			if callErr == nil || cw.n > before || (!router.Transient(callErr) && !errors.Is(callErr, streamguard.ErrPrelude)) || attempt == 1 {
				break
			}
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			return Call{Account: lastAccount, Deployment: model}, nil
		}
		last = callErr
		proceed, returned := g.noteStreamAttempt(callErr, cw.n > 0, inst.Provider.ID, model, &st)
		if returned != nil {
			return Call{Account: lastAccount, Deployment: model}, returned
		}
		if proceed {
			continue
		}
		return Call{Account: lastAccount, Deployment: model}, callErr
	}
	// An account skipped for being slow or busy was not cooled down, so saying
	// "every account is cooling" would be a lie.
	if st.SlowPrelude || st.AdmissionSkipped {
		return Call{Account: lastAccount, Deployment: model}, last
	}
	if st.Refused && (protocolRefused(last) || errors.Is(last, errModelRefusesChat)) {
		return Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	return Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
}

// EditImage proxies POST /v1/images/edits for the same image_out accounts
// that serve generations. Multipart bodies are forwarded with their content type.
func (g *Gateway) EditImage(ctx context.Context, raw []byte, contentType string) (adapter.ImageResponse, Call, error) {
	// Same deadline and attempt budget as Chat. These paths used to have
	// neither, so a hung provider held the request open indefinitely and the
	// attempt budget did not apply (#67).
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model := ImageEditModel(raw, contentType)
	body := raw
	client := model
	if !strings.Contains(strings.ToLower(contentType), "multipart/") {
		var err error
		model, client, body, _, err = g.prepare(raw)
		if err != nil {
			return adapter.ImageResponse{}, Call{}, err
		}
	} else if target, ok := g.routeTarget(model); ok && target != model {
		if rewritten, ct, ok := rewriteMultipartModel(raw, contentType, model, target); ok {
			body, contentType, model = rewritten, ct, target
		}
	}
	if model == "" {
		return adapter.ImageResponse{}, Call{}, adapter.ErrImageModelRequired
	}
	if !g.supportsImageOut(model) {
		return adapter.ImageResponse{}, Call{}, adapter.ErrModelNotImageOut
	}
	model, cands, session, err := g.routeResolved(ctx, body, model)
	if err != nil {
		return adapter.ImageResponse{}, Call{}, err
	}
	var last error
	var lastAccount string
	var st failState
	tried := false
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for _, inst := range cands {
		if err := budgetAttempts.take(); err != nil {
			return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		if !strings.Contains(strings.ToLower(contentType), "multipart/") {
			model, body = inst.applyModel(model, body)
		}
		ed, ok := inst.Adapter.(adapter.ImageEditor)
		if !ok || !effectiveCapabilities(inst).ImageOut {
			last = adapter.ErrImageOutUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		resp, callErr := admittedSlot(g, ctx, inst.Provider.ID, func(c context.Context) (adapter.ImageResponse, error) {
			return ed.EditImage(c, adapter.ImageRequest{Model: model, Raw: body, ContentType: contentType})
		})
		if stop := ambiguousDelivery(callErr); stop != nil {
			return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, stop
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			if !strings.Contains(strings.ToLower(contentType), "multipart/") {
				resp.Raw = echoClientModel(resp.Raw, client, model)
			}
			return resp, Call{Account: lastAccount, Deployment: model}, nil
		}
		last = callErr
		if g.noteAttempt(callErr, inst.Provider.ID, model, &st, failPolicy{Replay: replayableAfter}) {
			continue
		}
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, callErr
	}
	if !tried {
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, adapter.ErrImageOutUnsupported
	}
	if retryable(last) || router.Transient(last) {
		if st.AdmissionSkipped {
			return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, last
		}
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, last
}

func (g *Gateway) routeTarget(name string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	target, ok := g.cfg.Routes[strings.TrimSpace(name)]
	if !ok {
		return "", false
	}
	target = strings.TrimSpace(target)
	if _, found := catalog.FindRoutable(g.models, g.queryLocked(), target); !found {
		return "", false
	}
	return target, true
}

// GenerateImage proxies OpenAI-compatible POST /v1/images/generations.
// Upstream is only called when the model is tagged image_out and the adapter
// implements ImageGenerator with Capabilities.ImageOut.
func (g *Gateway) GenerateImage(ctx context.Context, raw []byte) (adapter.ImageResponse, Call, error) {
	// Same deadline and attempt budget as Chat. These paths used to have
	// neither, so a hung provider held the request open indefinitely and the
	// attempt budget did not apply (#67).
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	_ = budget
	if err != nil {
		return adapter.ImageResponse{}, Call{}, err
	}
	if model == "" {
		return adapter.ImageResponse{}, Call{}, adapter.ErrImageModelRequired
	}
	if !g.supportsImageOut(model) {
		return adapter.ImageResponse{}, Call{}, adapter.ErrModelNotImageOut
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.ImageResponse{}, Call{}, err
	}
	var last error
	var lastAccount string
	var st failState
	tried := false
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for _, inst := range cands {
		if err := budgetAttempts.take(); err != nil {
			return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		model, raw := inst.applyModel(model, raw)
		gen, ok := inst.Adapter.(adapter.ImageGenerator)
		if !ok || !effectiveCapabilities(inst).ImageOut {
			last = adapter.ErrImageOutUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		resp, callErr := admittedSlot(g, ctx, inst.Provider.ID, func(c context.Context) (adapter.ImageResponse, error) {
			return gen.GenerateImage(c, adapter.ImageRequest{Model: model, Raw: raw})
		})
		if stop := ambiguousDelivery(callErr); stop != nil {
			return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, stop
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			return resp, Call{Account: lastAccount, Deployment: model}, nil
		}
		last = callErr
		if g.noteAttempt(callErr, inst.Provider.ID, model, &st, failPolicy{Replay: replayableAfter}) {
			continue
		}
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, callErr
	}
	if !tried {
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, adapter.ErrImageOutUnsupported
	}
	if st.AdmissionSkipped {
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, last
	}
	if retryable(last) {
		return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	return adapter.ImageResponse{}, Call{Account: lastAccount, Deployment: model}, last
}

func (g *Gateway) supportsImageOut(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	if m, ok := catalog.FindRoutable(g.models, q, id); ok {
		return catalog.HasModality(m, "image_out")
	}
	return slices.Contains(catalog.InferModalities(id), "image_out")
}

// CreateEmbeddings proxies OpenAI-compatible POST /v1/embeddings.
// Upstream is only called when the model is tagged embeddings and the adapter
// implements Embedder with Capabilities.Embeddings.
func (g *Gateway) CreateEmbeddings(ctx context.Context, raw []byte) (adapter.EmbeddingResponse, Call, error) {
	// Same deadline and attempt budget as Chat. These paths used to have
	// neither, so a hung provider held the request open indefinitely and the
	// attempt budget did not apply (#67).
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	_ = budget
	if err != nil {
		return adapter.EmbeddingResponse{}, Call{}, err
	}
	if model == "" {
		return adapter.EmbeddingResponse{}, Call{}, adapter.ErrEmbeddingModelRequired
	}
	if !g.supportsEmbeddings(model) {
		return adapter.EmbeddingResponse{}, Call{}, adapter.ErrModelNotEmbeddings
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.EmbeddingResponse{}, Call{}, err
	}
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if hit, ok := g.cachedEmbeddings(inst.Provider.ID, inst.Provider.BaseURL, model, raw); ok {
			hit.Raw = echoClientModel(hit.Raw, client, model)
			return hit, Call{Account: inst.Provider.ID, Deployment: model}, nil
		}
	}
	var last error
	var lastAccount string
	var st failState
	tried := false
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for _, inst := range cands {
		if err := budgetAttempts.take(); err != nil {
			return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		model, raw := inst.applyModel(model, raw)
		emb, ok := inst.Adapter.(adapter.Embedder)
		if !ok || !effectiveCapabilities(inst).Embeddings {
			last = adapter.ErrEmbeddingsUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		var resp adapter.EmbeddingResponse
		var callErr error
		callUpstream := func(runCtx context.Context) (adapter.EmbeddingResponse, error) {
			var once adapter.EmbeddingResponse
			var onceErr error
			for attempt := 0; attempt < 2; attempt++ {
				once, onceErr = emb.CreateEmbeddings(runCtx, adapter.EmbeddingRequest{Model: model, Raw: raw})
				if onceErr == nil || !router.Transient(onceErr) || attempt == 1 {
					break
				}
			}
			return once, onceErr
		}
		if g.flight != nil && g.live().RequestEngine.CacheEmbeddings && responsecache.Eligible("embeddings", raw, true) {
			var body []byte
			var shared bool
			body, shared, callErr = g.flight.DoShared(ctx, responsecache.Key(inst.Provider.ID, inst.Provider.BaseURL, model, "embeddings", raw), func(runCtx context.Context) ([]byte, error) {
				once, err := admitted(g, runCtx, inst.Provider.ID, model, raw, func(c context.Context) (adapter.EmbeddingResponse, error) {
					return callUpstream(c)
				})
				if err != nil {
					return nil, err
				}
				return once.Raw, nil
			})
			if callErr == nil {
				resp.Raw, resp.CacheHit = body, shared
			}
		} else {
			resp, callErr = admitted(g, ctx, inst.Provider.ID, model, raw, func(c context.Context) (adapter.EmbeddingResponse, error) {
				return callUpstream(c)
			})
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			g.storeEmbeddings(lastAccount, inst.Provider.BaseURL, model, raw, resp.Raw)
			return resp, Call{Account: lastAccount, Deployment: model}, nil
		}
		last = callErr
		if g.noteAttempt(callErr, inst.Provider.ID, model, &st, failPolicy{}) {
			continue
		}
		return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, callErr
	}
	if !tried {
		return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, adapter.ErrEmbeddingsUnsupported
	}
	if st.AdmissionSkipped {
		return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, last
	}
	if retryable(last) {
		return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	return adapter.EmbeddingResponse{}, Call{Account: lastAccount, Deployment: model}, last
}

func (g *Gateway) supportsEmbeddings(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	if m, ok := catalog.FindRoutable(g.models, q, id); ok {
		return catalog.HasModality(m, "embeddings")
	}
	return slices.Contains(catalog.InferModalities(id), "embeddings")
}

// Responses uses native Responses when available, otherwise OpenAI chat translation.
func (g *Gateway) Responses(ctx context.Context, raw []byte) ([]byte, Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return nil, Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return nil, Call{}, err
	}
	cands = continuationFirst(cands, g.continuationAccount(previousResponseID(raw), model))
	if len(cands) == 0 {
		return nil, Call{}, router.ErrNoAccount
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, false)
		oaReq.Stream = false
	}
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			if err := budgetAttempts.take(); err != nil {
				return nil, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			out, err := admitted(g, ctx, inst.Provider.ID, model, raw, func(c context.Context) ([]byte, error) {
				return nr.Responses(c, jsonx.SetStream(raw, false))
			})
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				g.bindContinuation(responseID(out), model, lastAccount)
				return echoClientModel(out, client, model), Call{Account: lastAccount, Deployment: model}, nil
			}
			last = err
			if g.noteAttempt(err, inst.Provider.ID, model, &st, failPolicy{Protocol: true}) {
				continue
			}
			return nil, Call{Account: lastAccount, Deployment: model}, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		var resp adapter.ChatResponse
		var callErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := budgetAttempts.take(); err != nil {
				return nil, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			resp, callErr = admitted(g, ctx, inst.Provider.ID, oaReq.Model, oaReq.Raw, func(c context.Context) (adapter.ChatResponse, error) {
				return inst.Adapter.Chat(c, chatReq(inst, oaReq.Model, oaReq.Raw, false, budget))
			})
			if callErr == nil || !router.Transient(callErr) || attempt == 1 {
				break
			}
		}
		if callErr != nil {
			last = callErr
			if g.noteAttempt(callErr, inst.Provider.ID, model, &st, failPolicy{Protocol: true}) {
				continue
			}
			return nil, Call{Account: lastAccount, Deployment: model}, callErr
		}
		if len(resp.Raw) > 0 {
			out, ferr := translate.FromOpenAIChat(resp.Raw, client)
			if ferr == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return echoClientModel(out, client, model), Call{Account: lastAccount, Deployment: model}, nil
			}
		}
		out, err := translate.FromChatContent(resp.ID, client, resp.Content)
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
		}
		return echoClientModel(out, client, model), Call{Account: lastAccount, Deployment: model}, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if st.Refused {
		return nil, Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	if st.AdmissionSkipped {
		return nil, Call{Account: lastAccount, Deployment: model}, last
	}
	if retryable(last) {
		return nil, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	return nil, Call{Account: lastAccount, Deployment: model}, last
}

// ResponsesStream writes Responses SSE (native pass-through or converted OpenAI stream).
func (g *Gateway) ResponsesStream(ctx context.Context, raw []byte, w io.Writer) (Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return Call{}, err
	}
	// A follow-up carrying previous_response_id has to return to the account
	// that holds that response, streamed or not.
	cands = continuationFirst(cands, g.continuationAccount(previousResponseID(raw), model))
	if len(cands) == 0 {
		return Call{}, router.ErrNoAccount
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	watch := newResponseIDWatcher(cw)
	guard := streamguard.New(watch, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	reachable := lastReachable[adapter.NativeResponses](cands, xerr == nil)
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if g.notChatModel(inst.Provider.ID, model) {
			last = errModelRefusesChat
			st.Refused = true
			continue
		}
		dest := newRouteRewriter(guard, model, client)
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			if err := budgetAttempts.take(); err != nil {
				return Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			guard.Reset()
			watch.reset()
			if budgetAttempts.final(i, reachable) {
				guard.Unbounded()
			}
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			// The hold and the slot are taken around the upstream call, not
			// around the write to the client: this branch streams straight from
			// the provider to dest, so calling the adapter directly left the
			// native Responses stream outside both guards that every other
			// attempt goes through.
			streamBody := jsonx.SetStream(raw, true)
			var streamErr error
			_, streamErr = admitted(g, attemptCtx, inst.Provider.ID, model, streamBody, func(c context.Context) (struct{}, error) {
				streamErr = noteStream(guard, flushAfter(dest, nr.ResponsesStream(c, streamBody, dest)))
				return struct{}{}, streamErr
			})
			err := streamErr
			stop()
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				// The same binding the non-streaming path does, so a follow-up
				// carrying previous_response_id returns to this account (#78).
				g.bindContinuation(watch.lastID(), model, lastAccount)
				return Call{Account: lastAccount, Deployment: model}, nil
			}
			last = err
			proceed, returned := g.noteStreamAttempt(err, cw.n > 0, inst.Provider.ID, model, &st)
			if returned != nil {
				return Call{Account: lastAccount, Deployment: model}, returned
			}
			if proceed {
				continue
			}
			return Call{Account: lastAccount, Deployment: model}, err
		}
		if xerr != nil {
			if !slowPrelude(last) {
				last = xerr
			}
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		guard.Reset()
		watch.reset()
		if budgetAttempts.final(i, reachable) {
			guard.Unbounded()
		}
		attemptCtx, stop := guard.Bound(ctx)
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			// Through dest, not guard: the rewriter is what puts the client's
			// own model id back into the translated events (#79).
			errCh <- translate.OpenAISSEToResponses(pr, dest, model)
			_ = pr.Close()
		}()
		inst.recordAttempt(&lastAccount)
		_, err := admitted(g, attemptCtx, inst.Provider.ID, oaReq.Model, oaReq.Raw, func(c context.Context) (struct{}, error) {
			return struct{}{}, noteStream(guard, inst.Adapter.ChatStream(c, chatReq(inst, oaReq.Model, oaReq.Raw, true, budget), pw))
		})
		stop()
		// A failed call reaches the translator as a read error, not a clean EOF
		// (nil closes normally), so it never completes the partial turn.
		_ = pw.CloseWithError(err)
		convErr := <-errCh
		// An error chunk that arrived before anything reached the client is a
		// failed attempt, not a turn to hand back (#50). The adapter closed
		// cleanly, so without this the account is credited with a success and
		// the error goes to a client that has seen nothing at all.
		//
		// It fails over without a cooldown: an error chunk carries no status to
		// classify, and it is not evidence that this account is unhealthy --
		// the next one answering the same model is.
		if err == nil && convErr != nil && cw.n == 0 {
			last = convErr
			continue
		}
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			// The translator mints its own response id, so this binds whatever
			// the client was just told to quote back in a follow-up (#78).
			g.bindContinuation(watch.lastID(), model, lastAccount)
			return Call{Account: lastAccount, Deployment: model}, convErr
		}
		last = err
		proceed, returned := g.noteStreamAttempt(err, cw.n > 0, inst.Provider.ID, model, &st)
		if returned != nil {
			return Call{Account: lastAccount, Deployment: model}, returned
		}
		if proceed {
			continue
		}
		return Call{Account: lastAccount, Deployment: model}, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) && !st.SlowPrelude && !st.AdmissionSkipped {
		return Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	if st.Refused && (protocolRefused(last) || errors.Is(last, errModelRefusesChat)) {
		return Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	return Call{Account: lastAccount, Deployment: model}, last
}

// ClaudeChat uses native Messages when available, otherwise OpenAI translation.
func (g *Gateway) ClaudeChat(ctx context.Context, raw []byte) ([]byte, Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return nil, Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return nil, Call{}, err
	}
	oaBody, oaReq, xerr := translate.ToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = oaBody
		oaReq.Stream = false
	}
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			if err := budgetAttempts.take(); err != nil {
				return nil, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			out, err := admitted(g, ctx, inst.Provider.ID, model, raw, func(c context.Context) ([]byte, error) {
				return nm.Messages(c, jsonx.SetStream(claudeRaw(g.promptBody(raw, inst.Provider.Adapter), budget), false))
			})
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return echoClientModel(out, client, model), Call{Account: lastAccount, Deployment: model}, nil
			}
			last = err
			if g.noteAttempt(err, inst.Provider.ID, model, &st, failPolicy{Protocol: true}) {
				continue
			}
			return nil, Call{Account: lastAccount, Deployment: model}, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return nil, Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		inst.recordAttempt(&lastAccount)
		resp, err := admitted(g, ctx, inst.Provider.ID, oaReq.Model, oaReq.Raw, func(c context.Context) (adapter.ChatResponse, error) {
			return inst.Adapter.Chat(c, chatReq(inst, oaReq.Model, oaReq.Raw, false, budget))
		})
		if err != nil {
			last = err
			if g.noteAttempt(err, inst.Provider.ID, model, &st, failPolicy{Protocol: true}) {
				continue
			}
			return nil, Call{Account: lastAccount, Deployment: model}, err
		}
		oaRaw := resp.Raw
		if len(oaRaw) == 0 {
			oaRaw, err = json.Marshal(openAIShim{
				ID:    "peaproxy",
				Model: oaReq.Model,
				Choices: []openAIChoice{{
					Message:      openAIMsg{Role: "assistant", Content: resp.Content},
					FinishReason: "stop",
				}},
			})
			if err != nil {
				return nil, Call{Account: lastAccount, Deployment: model}, err
			}
		}
		out, err := translate.FromOpenAI(oaRaw, client)
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
		}
		return echoClientModel(out, client, model), Call{Account: lastAccount, Deployment: model}, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if st.Refused {
		return nil, Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	if st.AdmissionSkipped {
		return nil, Call{Account: lastAccount, Deployment: model}, last
	}
	if retryable(last) {
		return nil, Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	return nil, Call{Account: lastAccount, Deployment: model}, last
}

// ClaudeChatStream writes true Anthropic SSE (native pass-through or converted OpenAI stream).
func (g *Gateway) ClaudeChatStream(ctx context.Context, raw []byte, w io.Writer) (Call, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return Call{}, err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return Call{}, err
	}
	oaBody, oaReq, xerr := translate.ToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	guard := streamguard.New(cw, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var st failState
	budgetAttempts := newAttemptCoordinator(g.live().RequestMaxAttempts())
	reachable := lastReachable[adapter.NativeMessages](cands, xerr == nil)
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if g.notChatModel(inst.Provider.ID, model) {
			last = errModelRefusesChat
			st.Refused = true
			continue
		}
		dest := newRouteRewriter(guard, model, client)
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			if err := budgetAttempts.take(); err != nil {
				return Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
			}
			guard.Reset()
			if budgetAttempts.final(i, reachable) {
				guard.Unbounded()
			}
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			_, err := admitted(g, attemptCtx, inst.Provider.ID, model, raw, func(c context.Context) (struct{}, error) {
				err := nm.MessagesStream(c, jsonx.SetStream(claudeRaw(g.promptBody(raw, inst.Provider.Adapter), budget), true), dest)
				return struct{}{}, noteStream(guard, flushAfter(dest, err))
			})
			stop()
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return Call{Account: lastAccount, Deployment: model}, nil
			}
			last = err
			proceed, returned := g.noteStreamAttempt(err, cw.n > 0, inst.Provider.ID, model, &st)
			if returned != nil {
				return Call{Account: lastAccount, Deployment: model}, returned
			}
			if proceed {
				continue
			}
			return Call{Account: lastAccount, Deployment: model}, err
		}
		if xerr != nil {
			if !slowPrelude(last) {
				last = xerr
			}
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return Call{Account: lastAccount, Deployment: model}, errAttemptBudgetExhausted
		}
		guard.Reset()
		if budgetAttempts.final(i, reachable) {
			guard.Unbounded()
		}
		attemptCtx, stop := guard.Bound(ctx)
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			// Through dest, not guard: message_start carries a model id, and
			// without the rewriter it is the upstream's (#79).
			errCh <- translate.OpenAISSEToClaude(pr, dest, model)
			_ = pr.Close()
		}()
		inst.recordAttempt(&lastAccount)
		_, err := admitted(g, attemptCtx, inst.Provider.ID, oaReq.Model, oaReq.Raw, func(c context.Context) (struct{}, error) {
			return struct{}{}, noteStream(guard, inst.Adapter.ChatStream(c, chatReq(inst, oaReq.Model, oaReq.Raw, true, budget), pw))
		})
		stop()
		// A failed call reaches the translator as a read error, not a clean EOF
		// (nil closes normally), so it never completes the partial turn.
		_ = pw.CloseWithError(err)
		convErr := <-errCh
		// An error chunk that arrived before anything reached the client is a
		// failed attempt, not a turn to hand back (#50). The adapter closed
		// cleanly, so without this the account is credited with a success and
		// the error goes to a client that has seen nothing at all.
		//
		// It fails over without a cooldown: an error chunk carries no status to
		// classify, and it is not evidence that this account is unhealthy --
		// the next one answering the same model is.
		if err == nil && convErr != nil && cw.n == 0 {
			last = convErr
			continue
		}
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			return Call{Account: lastAccount, Deployment: model}, convErr
		}
		last = err
		proceed, returned := g.noteStreamAttempt(err, cw.n > 0, inst.Provider.ID, model, &st)
		if returned != nil {
			return Call{Account: lastAccount, Deployment: model}, returned
		}
		if proceed {
			continue
		}
		return Call{Account: lastAccount, Deployment: model}, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) && !st.SlowPrelude && !st.AdmissionSkipped {
		return Call{Account: lastAccount, Deployment: model}, cooldownErr(last)
	}
	if st.Refused && (protocolRefused(last) || errors.Is(last, errModelRefusesChat)) {
		return Call{Account: lastAccount, Deployment: model}, errModelRefusesChat
	}
	return Call{Account: lastAccount, Deployment: model}, last
}

type openAIShim struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message      openAIMsg `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// cacheModel is the model half of a response-cache or in-flight key. prepare
// strips "-thinking-N" from the body before it is hashed, so the budget has to
// be part of the key or a thinking request shares a non-thinking answer.
func cacheModel(model string, budget int) string {
	if budget <= 0 {
		return model
	}
	return model + "\x00thinking=" + strconv.Itoa(budget)
}
