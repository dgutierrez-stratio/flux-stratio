package envvars

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func workload(kind, namespace, name string, containers []any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": containers,
				},
			},
		},
	}}
	return obj
}

func container(name string, env []any, envFrom []any) map[string]any {
	c := map[string]any{"name": name}
	if env != nil {
		c["env"] = env
	}
	if envFrom != nil {
		c["envFrom"] = envFrom
	}
	return c
}

func TestExtract_DirectValue(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "FOO", "value": "bar"},
		}, nil),
	})

	got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "bar" {
		t.Errorf("FOO = %q, want %q", got["FOO"], "bar")
	}
}

func TestExtract_EnvFromConfigMapMergesAllKeys(t *testing.T) {
	g := NewFakeGetter().WithConfigMap("ns", "cm", map[string]string{"A": "1", "B": "2"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", nil, []any{
			map[string]any{"configMapRef": map[string]any{"name": "cm"}},
		}),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "1" || got["B"] != "2" {
		t.Errorf("got = %v, want A=1 B=2", got)
	}
}

// TestExtract_EnvFromSecretMergesAllKeys: every key is there, but as a
// placeholder — a Secret's value never leaves envvars.
func TestExtract_EnvFromSecretMergesAllKeys(t *testing.T) {
	g := NewFakeGetter().WithSecret("ns", "sec", map[string]string{"TOKEN": "s3cr3t"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", nil, []any{
			map[string]any{"secretRef": map[string]any{"name": "sec"}},
		}),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["TOKEN"] != "<secret:sec/TOKEN>" {
		t.Errorf("TOKEN = %q, want %q", got["TOKEN"], "<secret:sec/TOKEN>")
	}
}

// TestExtract_EnvFromPrefix: the kubelet prepends an envFrom entry's
// prefix to each key it injects.
func TestExtract_EnvFromPrefix(t *testing.T) {
	g := NewFakeGetter().
		WithConfigMap("ns", "cm", map[string]string{"FOO": "bar"}).
		WithSecret("ns", "sec", map[string]string{"TOKEN": "s3cr3t"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", nil, []any{
			map[string]any{"configMapRef": map[string]any{"name": "cm"}, "prefix": "P_"},
			map[string]any{"secretRef": map[string]any{"name": "sec"}, "prefix": "S_"},
		}),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["P_FOO"] != "bar" || got["S_TOKEN"] != "<secret:sec/TOKEN>" {
		t.Errorf("got %v, want P_FOO=bar and S_TOKEN as a placeholder", got)
	}
	if _, ok := got["FOO"]; ok {
		t.Errorf("got the unprefixed FOO too: %v", got)
	}
}

func TestExtract_DirectEnvWinsOverEnvFrom(t *testing.T) {
	g := NewFakeGetter().WithConfigMap("ns", "cm", map[string]string{"FOO": "from-envFrom"})
	w := workload("Deployment", "ns", "app", []any{
		container("main",
			[]any{map[string]any{"name": "FOO", "value": "from-env"}},
			[]any{map[string]any{"configMapRef": map[string]any{"name": "cm"}}},
		),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "from-env" {
		t.Errorf("FOO = %q, want %q (direct env must win)", got["FOO"], "from-env")
	}
}

func TestExtract_ConfigMapKeyRef(t *testing.T) {
	g := NewFakeGetter().WithConfigMap("ns", "cm", map[string]string{"KEY": "value"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "FOO", "valueFrom": map[string]any{
				"configMapKeyRef": map[string]any{"name": "cm", "key": "KEY"},
			}},
		}, nil),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "value" {
		t.Errorf("FOO = %q, want %q", got["FOO"], "value")
	}
}

func TestExtract_SecretKeyRef(t *testing.T) {
	g := NewFakeGetter().WithSecret("ns", "sec", map[string]string{"PASSWORD": "hunter2"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "PW", "valueFrom": map[string]any{
				"secretKeyRef": map[string]any{"name": "sec", "key": "PASSWORD"},
			}},
		}, nil),
	})

	got, err := Extract(context.Background(), g, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["PW"] != "<secret:sec/PASSWORD>" {
		t.Errorf("PW = %q, want the placeholder, never the Secret's value", got["PW"])
	}
}

func TestExtract_FieldRefNamespaceAndName(t *testing.T) {
	w := workload("Deployment", "my-ns", "my-app", []any{
		container("main", []any{
			map[string]any{"name": "NS", "valueFrom": map[string]any{
				"fieldRef": map[string]any{"fieldPath": "metadata.namespace"},
			}},
			map[string]any{"name": "NAME", "valueFrom": map[string]any{
				"fieldRef": map[string]any{"fieldPath": "metadata.name"},
			}},
			map[string]any{"name": "OTHER", "valueFrom": map[string]any{
				"fieldRef": map[string]any{"fieldPath": "status.podIP"},
			}},
		}, nil),
	})

	got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["NS"] != "my-ns" || got["NAME"] != "my-app" || got["OTHER"] != "<status.podIP>" {
		t.Errorf("got = %v", got)
	}
}

func TestExtract_ResourceFieldRef(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "LIMIT", "valueFrom": map[string]any{
				"resourceFieldRef": map[string]any{"resource": "limits.memory"},
			}},
		}, nil),
	})

	got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["LIMIT"] != "<limits.memory>" {
		t.Errorf("LIMIT = %q, want %q", got["LIMIT"], "<limits.memory>")
	}
}

// TestExtract_MissingConfigMapWarnsAndYieldsPlaceholder: an unresolvable
// reference is never mistaken for a real empty value.
func TestExtract_MissingConfigMapWarnsAndYieldsPlaceholder(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "FOO", "valueFrom": map[string]any{
				"configMapKeyRef": map[string]any{"name": "missing-cm", "key": "K"},
			}},
		}, nil),
	})

	warnCount := 0
	got, err := Extract(context.Background(), NewFakeGetter(), w, func(string, ...any) {
		warnCount++
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "<unresolved:configMapKeyRef:missing-cm/K>" {
		t.Errorf("FOO = %q, want an unresolved placeholder", got["FOO"])
	}
	if warnCount != 1 {
		t.Fatalf("warnCount = %d, want exactly 1", warnCount)
	}
}

func TestExtract_MissingKeyInConfigMapWarns(t *testing.T) {
	g := NewFakeGetter().WithConfigMap("ns", "cm", map[string]string{"OTHER": "x"})
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "FOO", "valueFrom": map[string]any{
				"configMapKeyRef": map[string]any{"name": "cm", "key": "MISSING"},
			}},
		}, nil),
	})

	var warned bool
	got, err := Extract(context.Background(), g, w, func(string, ...any) { warned = true })
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "<unresolved:configMapKeyRef:cm/MISSING>" || !warned {
		t.Errorf("FOO = %q, warned = %v, want an unresolved placeholder and a warning", got["FOO"], warned)
	}
}

func TestExtract_NilWarnDoesNotPanic(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{
			map[string]any{"name": "FOO", "valueFrom": map[string]any{
				"configMapKeyRef": map[string]any{"name": "missing", "key": "K"},
			}},
		}, nil),
	})
	if _, err := Extract(context.Background(), NewFakeGetter(), w, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExtract_LaterContainerOverwritesEarlier(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("first", []any{map[string]any{"name": "FOO", "value": "first"}}, nil),
		container("second", []any{map[string]any{"name": "FOO", "value": "second"}}, nil),
	})

	got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "second" {
		t.Errorf("FOO = %q, want %q (last container wins)", got["FOO"], "second")
	}
}

func TestExtract_StatefulSetAndDaemonSetShapesWork(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		w := workload(kind, "ns", "app", []any{
			container("main", []any{map[string]any{"name": "FOO", "value": "bar"}}, nil),
		})
		got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got["FOO"] != "bar" {
			t.Errorf("%s: FOO = %q, want %q", kind, got["FOO"], "bar")
		}
	}
}

func TestExtract_ValueWithoutValueFromYieldsEmptyString(t *testing.T) {
	w := workload("Deployment", "ns", "app", []any{
		container("main", []any{map[string]any{"name": "FOO"}}, nil),
	})
	got, err := Extract(context.Background(), NewFakeGetter(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["FOO"]; !ok || v != "" {
		t.Errorf("FOO = %q (present=%v), want empty string present", v, ok)
	}
}
