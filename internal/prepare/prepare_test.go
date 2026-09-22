package prepare

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
)

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestFind(t *testing.T) {
	for _, name := range []string{"prepare-datamarket-agent", "prepare-datarest", "prepare-dlc", "prepare-genai"} {
		if Find(name) == nil {
			t.Errorf("Find(%q) = nil, want a step", name)
		}
	}
	if Find("does-not-exist") != nil {
		t.Error("Find(\"does-not-exist\") = non-nil, want nil")
	}
}

func TestSteps_AutomatedFlags(t *testing.T) {
	want := map[string]bool{
		"prepare-datamarket-agent": true,
		"prepare-datarest":         true,
		"prepare-dlc":              true,
		"prepare-genai":            false,
	}
	for name, wantAutomated := range want {
		step := Find(name)
		if step == nil {
			t.Fatalf("Find(%q) = nil", name)
		}
		if step.Automated != wantAutomated {
			t.Errorf("%s.Automated = %v, want %v", name, step.Automated, wantAutomated)
		}
	}
}
