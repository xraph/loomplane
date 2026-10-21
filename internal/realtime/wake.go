package realtime

// WakeSource exposes subscriptions without transferring Bus lifecycle ownership
// to Forge's dependency container. The realtime extension starts and stops Bus.
type WakeSource func(org string) (<-chan struct{}, func())

func (s WakeSource) Watch(org string) (wake <-chan struct{}, unsubscribe func()) { return s(org) }
