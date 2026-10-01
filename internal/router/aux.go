package router

import (
	"context"
	"errors"
	"time"

	"github.com/realblxckcodex/fylgja/internal/llm"
)

// auxCandidates liefert Deployments eines Modells in Score-Reihenfolge (ohne Slot-Reservierung
// für kurze Hilfsaufrufe wie Embeddings/STT, die nicht über die Chat-Queue laufen).
func (r *Router) auxCandidates(model string, privacy llm.Privacy) []*Deployment {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	var out []*Deployment
	for _, d := range r.deps {
		if d.Model == model && d.State == Ready && d.SatisfiesPrivacy(privacy) && r.stats[d.Name].breaker.allow(now) {
			out = append(out, d)
		}
	}
	return out
}

func (r *Router) auxDone(d *Deployment, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.stats[d.Name]; s != nil {
		if err != nil {
			s.errors++
			s.breaker.failure(r.clock.Now())
		} else {
			s.served++
			s.breaker.success()
		}
	}
}

var ErrNoAux = errors.New("router: kein deployment für dieses modell")

// Embed nutzt das logische Modell (z. B. "embedder") – Privacy: immer self_hosted, außer model ist explizit freigegeben.
func (r *Router) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	var last error = ErrNoAux
	for _, d := range r.auxCandidates(model, llm.SelfHostedOnly) {
		e, ok := d.Client.(llm.Embedder)
		if !ok {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		v, err := e.Embed(c, d.ServedModel, inputs)
		cancel()
		r.auxDone(d, err)
		if err == nil {
			return v, nil
		}
		last = err
	}
	return nil, last
}

// Transcribe nutzt das logische Modell "stt".
func (r *Router) Transcribe(ctx context.Context, model string, audio []byte, mime string) (string, error) {
	var last error = ErrNoAux
	for _, d := range r.auxCandidates(model, llm.SelfHostedOnly) {
		t, ok := d.Client.(llm.Transcriber)
		if !ok {
			continue
		}
		txt, err := t.Transcribe(ctx, d.ServedModel, audio, mime)
		r.auxDone(d, err)
		if err == nil {
			return txt, nil
		}
		last = err
	}
	return "", last
}

// Speak nutzt das logische Modell "tts" (Text → Sprache).
func (r *Router) Speak(ctx context.Context, model, voice, text, format string) ([]byte, error) {
	var last error = ErrNoAux
	for _, d := range r.auxCandidates(model, llm.SelfHostedOnly) {
		sp, ok := d.Client.(llm.Speaker)
		if !ok {
			continue
		}
		b, err := sp.Speak(ctx, d.ServedModel, voice, text, format)
		r.auxDone(d, err)
		if err == nil {
			return b, nil
		}
		last = err
	}
	return nil, last
}

// HasModel meldet, ob ein logisches Modell im Katalog ist.
func (r *Router) HasModel(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.models[name]
	return ok
}
