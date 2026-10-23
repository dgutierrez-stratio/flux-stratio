package envvars

import (
	"context"
	"fmt"
)

// mapKey identifies one ConfigMap or Secret by namespace and name.
type mapKey struct{ namespace, name string }

// FakeGetter is a Getter for tests: canned ConfigMap/Secret data keyed by
// namespace/name, with no live cluster involved.
type FakeGetter struct {
	ConfigMaps map[mapKey]map[string]string
	Secrets    map[mapKey]map[string]string
}

// NewFakeGetter returns an empty FakeGetter ready for its With* methods.
func NewFakeGetter() *FakeGetter {
	return &FakeGetter{
		ConfigMaps: map[mapKey]map[string]string{},
		Secrets:    map[mapKey]map[string]string{},
	}
}

// WithConfigMap registers data for a ConfigMap and returns the receiver,
// for chaining.
func (f *FakeGetter) WithConfigMap(namespace, name string, data map[string]string) *FakeGetter {
	f.ConfigMaps[mapKey{namespace, name}] = data
	return f
}

// WithSecret registers data for a Secret and returns the receiver, for
// chaining.
func (f *FakeGetter) WithSecret(namespace, name string, data map[string]string) *FakeGetter {
	f.Secrets[mapKey{namespace, name}] = data
	return f
}

// ConfigMap implements Getter.
func (f *FakeGetter) ConfigMap(_ context.Context, namespace, name string) (map[string]string, error) {
	data, ok := f.ConfigMaps[mapKey{namespace, name}]
	if !ok {
		return nil, fmt.Errorf("configmap %s/%s not found", namespace, name)
	}
	return data, nil
}

// Secret implements Getter.
func (f *FakeGetter) Secret(_ context.Context, namespace, name string) (map[string]string, error) {
	data, ok := f.Secrets[mapKey{namespace, name}]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s not found", namespace, name)
	}
	return data, nil
}
