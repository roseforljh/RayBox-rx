package internet

import "context"

type systemDialerContextKey struct{}
type ownerContextKey struct{}

func ContextWithOwner(ctx, owner context.Context) context.Context {
	return context.WithValue(ctx, ownerContextKey{}, owner)
}

func OwnerContext(ctx context.Context) context.Context {
	if owner, ok := ctx.Value(ownerContextKey{}).(context.Context); ok {
		return owner
	}
	return ctx
}

// ContextWithSystemDialer scopes protected system dialing to one request.
func ContextWithSystemDialer(ctx context.Context, dialer SystemDialer) context.Context {
	return context.WithValue(ctx, systemDialerContextKey{}, dialer)
}

// CopySystemDialer propagates the owner's protected dialer into transport-created contexts.
func CopySystemDialer(ctx, source context.Context) context.Context {
	if dialer, ok := source.Value(systemDialerContextKey{}).(SystemDialer); ok && dialer != nil {
		return ContextWithSystemDialer(ctx, dialer)
	}
	return ctx
}

func systemDialerFromContext(ctx context.Context) SystemDialer {
	if dialer, ok := ctx.Value(systemDialerContextKey{}).(SystemDialer); ok && dialer != nil {
		return dialer
	}
	return effectiveSystemDialer
}
