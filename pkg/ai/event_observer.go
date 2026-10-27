package ai

import "context"

type eventObserverKey struct{}

// WithEventObserver observes diagnostic events from buffered execution.
func WithEventObserver(ctx context.Context, observer func(Event)) context.Context {
	return context.WithValue(ctx, eventObserverKey{}, observer)
}

func ReportEvent(ctx context.Context, event Event) {
	if observer, ok := ctx.Value(eventObserverKey{}).(func(Event)); ok && observer != nil {
		observer(event)
	}
}
