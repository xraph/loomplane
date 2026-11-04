package metering

import "github.com/xraph/forge"

// Resolve returns the container's Gate and Meter, or AllowAll and Discard
// when the edition provides none.
func Resolve(c forge.Container) (Gate, Meter, error) {
	gate, meter := AllowAll, Discard
	if forge.HasType[Gate](c) {
		g, err := forge.InjectType[Gate](c)
		if err != nil {
			return nil, nil, err
		}
		gate = g
	}
	if forge.HasType[Meter](c) {
		m, err := forge.InjectType[Meter](c)
		if err != nil {
			return nil, nil, err
		}
		meter = m
	}
	return gate, meter, nil
}
