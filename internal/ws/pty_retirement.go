package ws

import "context"

type ptyInputRetirementKey struct{}

// WithPTYInputRetirement binds the registration's idempotent cleanup to its
// consumer. Retirement stops new admissions before the consumer drains accepted
// messages; the registration owner can safely call cleanup again afterward.
func WithPTYInputRetirement(ctx context.Context, retire func()) context.Context {
	return context.WithValue(ctx, ptyInputRetirementKey{}, retire)
}

// RetirePTYInput stops admission to this consumer's original registration.
// It cannot retire a replacement session registered under the same ID.
func RetirePTYInput(ctx context.Context) {
	if retire, ok := ctx.Value(ptyInputRetirementKey{}).(func()); ok {
		retire()
	}
}
