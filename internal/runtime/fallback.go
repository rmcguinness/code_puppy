package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"

	"github.com/retail-cortex/code_puppy/internal/breaker"
	"google.golang.org/adk/v2/model"
)

// FallbackFromKey is set in a response's CustomMetadata when a fallback
// model answered; its value is the name of the model that failed first.
const FallbackFromKey = "code_puppy_fallback_from"

// modelFailThreshold: SDK retries have already run by the time a call
// fails, so one failure is enough to prefer the next model for a while.
const modelFailThreshold = 1

// fallbackModel tries models in order (llm.fallback_models). A model that
// fails is skipped for a cooldown (its circuit breaker), so a provider
// outage doesn't cost a failed call on every step; the primary is tried
// again when its cooldown ends.
//
// It only moves on when a model fails before producing anything: once
// output has been yielded (streamed text is on screen) an error ends the
// call, as it would without fallbacks. Cancellation never moves on.
type fallbackModel struct {
	chain  []model.LLM
	health []*breaker.Breaker
}

func newFallbackModel(chain []model.LLM) model.LLM {
	if len(chain) == 1 {
		return chain[0]
	}
	f := &fallbackModel{chain: chain, health: make([]*breaker.Breaker, len(chain))}
	for i := range chain {
		f.health[i] = breaker.New(modelFailThreshold)
	}
	return f
}

// Name is the primary model's name: the one configured and shown.
func (f *fallbackModel) Name() string { return f.chain[0].Name() }

// Models returns the names in fallback order.
func (f *fallbackModel) Models() []string {
	names := make([]string, len(f.chain))
	for i, m := range f.chain {
		names[i] = m.Name()
	}
	return names
}

func (f *fallbackModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var errs []error
		// First pass: models whose breakers allow a call. Allow is asked
		// just before each attempt because it reserves a half-open trial.
		// If every breaker was open, try them all rather than fail unasked.
		for _, force := range []bool{false, true} {
			tried := false
			for i, m := range f.chain {
				if !force {
					if ok, _ := f.health[i].Allow(); !ok {
						continue
					}
				}
				tried = true
				done, err := f.try(ctx, i, m, req, stream, yield)
				if done {
					return
				}
				errs = append(errs, fmt.Errorf("%s: %w", m.Name(), err))
			}
			if tried {
				break
			}
		}
		yield(nil, fmt.Errorf("every model failed: %w", errors.Join(errs...)))
	}
}

// try runs one model. done means the call is over (answered, failed after
// producing output, cancelled, or the consumer stopped); otherwise err is
// why this model failed before producing anything.
func (f *fallbackModel) try(ctx context.Context, i int, m model.LLM, req *model.LLMRequest, stream bool, yield func(*model.LLMResponse, error) bool) (done bool, err error) {
	produced := false
	for resp, err := range m.GenerateContent(ctx, req, stream) {
		if err != nil {
			switch {
			case ctx.Err() != nil || errors.Is(err, context.Canceled):
				f.health[i].Abandon() // no verdict on the model
			case produced: // too late to switch: part of the answer is out
				f.health[i].Failure()
			default:
				f.health[i].Failure()
				slog.WarnContext(ctx, "model failed; trying the next fallback", "model", m.Name(), "error", err)
				return false, err
			}
			yield(nil, err)
			return true, nil
		}
		produced = true
		if i > 0 && resp != nil {
			markFallback(resp, m.Name(), f.chain[0].Name())
		}
		if !yield(resp, nil) {
			f.health[i].Success() // it was answering; the consumer stopped
			return true, nil
		}
	}
	f.health[i].Success()
	return true, nil
}

// markFallback records which model answered, so usage is priced by it and
// the user can be told.
func markFallback(resp *model.LLMResponse, served, primary string) {
	if resp.ModelVersion == "" {
		resp.ModelVersion = served
	}
	if resp.CustomMetadata == nil {
		resp.CustomMetadata = map[string]any{}
	}
	resp.CustomMetadata[FallbackFromKey] = primary
}
