package provider

import (
	"context"
	"iter"
	"reflect"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// sessionStatePreservingAgent projects state mutations made by a wrapped
// provider back into runner-visible event deltas. The structured runtime
// wrapper aggregates provider events, so provider-owned bindings would
// otherwise remain invocation-local and be recreated on the next Plan call.
type sessionStatePreservingAgent struct {
	adkagent.Agent
}

func preserveSessionState(agent adkagent.Agent) adkagent.Agent {
	return &sessionStatePreservingAgent{Agent: agent}
}

func (agent *sessionStatePreservingAgent) Run(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		before := snapshotSessionState(ctx)
		statePersisted := false
		for event, runErr := range agent.Agent.Run(ctx) {
			if runErr != nil {
				if !yieldSessionStateDelta(ctx, before, yield) {
					return
				}
				yield(nil, runErr)
				return
			}
			if event != nil && event.TurnComplete {
				mergeSessionStateDelta(event, changedSessionState(ctx, before))
				statePersisted = true
			}
			if !yield(event, nil) {
				return
			}
		}
		if !statePersisted {
			yieldSessionStateDelta(ctx, before, yield)
		}
	}
}

func snapshotSessionState(ctx adkagent.InvocationContext) map[string]any {
	values := make(map[string]any)
	if ctx == nil || ctx.Session() == nil || ctx.Session().State() == nil {
		return values
	}
	for key, value := range ctx.Session().State().All() {
		values[key] = value
	}
	return values
}

func changedSessionState(ctx adkagent.InvocationContext, before map[string]any) map[string]any {
	after := snapshotSessionState(ctx)
	delta := make(map[string]any)
	for key, value := range after {
		if previous, ok := before[key]; !ok || !reflect.DeepEqual(previous, value) {
			delta[key] = value
		}
	}
	return delta
}

func mergeSessionStateDelta(event *session.Event, delta map[string]any) {
	if event == nil || len(delta) == 0 {
		return
	}
	if event.Actions.StateDelta == nil {
		event.Actions.StateDelta = make(map[string]any, len(delta))
	}
	for key, value := range delta {
		event.Actions.StateDelta[key] = value
	}
}

func yieldSessionStateDelta(
	ctx adkagent.InvocationContext,
	before map[string]any,
	yield func(*session.Event, error) bool,
) bool {
	delta := changedSessionState(ctx, before)
	if len(delta) == 0 {
		return true
	}
	event := session.NewEvent(context.Background(), ctx.InvocationID())
	mergeSessionStateDelta(event, delta)
	return yield(event, nil)
}
