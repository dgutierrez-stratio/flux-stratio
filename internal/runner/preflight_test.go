package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreflight_AllPresentAndAboveMinimum(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux-operator": {Stdout: []byte("flux-operator version 0.45.1\n")},
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("v3.16.2+g13654a5\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.34.2\n")},
	}}

	report := Preflight(context.Background(), f)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
	if err := report.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
	if len(report.Statuses) != len(Required) {
		t.Fatalf("len(Statuses) = %d, want %d", len(report.Statuses), len(Required))
	}
	for _, s := range report.Statuses {
		if !s.Found {
			t.Errorf("%s: Found = false, want true", s.Binary.Name)
		}
		if s.Version == "" {
			t.Errorf("%s: Version = %q, want a parsed version", s.Binary.Name, s.Version)
		}
	}
}

func TestPreflight_MissingBinary(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux": {Stdout: []byte("flux version 2.9.2\n")},
		"helm": {Stdout: []byte("v3.16.2\n")},
		// flux-operator and kubectl deliberately unconfigured -> Fake.Run errors for them.
	}}

	report := Preflight(context.Background(), f)

	if report.OK() {
		t.Fatal("report.OK() = true, want false (flux-operator and kubectl are missing)")
	}
	err := report.Err()
	if err == nil {
		t.Fatal("Err() = nil, want a summary error")
	}
	for _, want := range []string{"flux-operator", "kubectl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Err() = %q, want it to mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "helm:") || strings.Contains(err.Error(), "  - flux:") {
		t.Errorf("Err() = %q, should not report the two binaries that were found", err)
	}
}

func TestPreflight_BelowMinimumVersion(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux-operator": {Stdout: []byte("flux-operator version 0.40.0\n")}, // below the 0.45.1 minimum
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("v3.16.2\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.34.2\n")},
	}}

	report := Preflight(context.Background(), f)

	if report.OK() {
		t.Fatal("report.OK() = true, want false (flux-operator is below its minimum version)")
	}
	err := report.Err()
	if err == nil || !strings.Contains(err.Error(), "flux-operator") || !strings.Contains(err.Error(), "0.40.0") {
		t.Errorf("Err() = %v, want it to name flux-operator and its detected version 0.40.0", err)
	}
}

func TestPreflight_NoMinimumForHelmAndKubectl(t *testing.T) {
	// helm and kubectl declare no MinVersion in Required, so any parseable
	// version must pass, however old.
	f := &Fake{Responses: map[string]FakeResponse{
		"flux-operator": {Stdout: []byte("flux-operator version 0.45.1\n")},
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("v2.0.0\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.10.0\n")},
	}}

	report := Preflight(context.Background(), f)

	if !report.OK() {
		t.Errorf("report.OK() = false, want true (no minimum declared for helm/kubectl); error: %v", report.Err())
	}
}

func TestPreflight_UnparseableVersionIsNotAFailure(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux-operator": {Stdout: []byte("flux-operator version 0.45.1\n")},
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("unexpected output with no version-looking substring\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.34.2\n")},
	}}

	report := Preflight(context.Background(), f)

	if !report.OK() {
		t.Errorf("report.OK() = false, want true (unparseable output should degrade gracefully); error: %v", report.Err())
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got  [3]string
		min  string
		want bool
	}{
		{[3]string{"2", "9", "2"}, "2.9.0", true},
		{[3]string{"2", "9", "0"}, "2.9.0", true},
		{[3]string{"2", "8", "9"}, "2.9.0", false},
		{[3]string{"1", "0", "0"}, "2.9.0", false},
		{[3]string{"3", "0", "0"}, "2.9.0", true},
		{[3]string{"0", "45", "1"}, "0.45.1", true},
		{[3]string{"0", "45", "0"}, "0.45.1", false},
	}
	for _, c := range cases {
		got, err := versionAtLeast(c.got[:], c.min)
		if err != nil {
			t.Fatalf("versionAtLeast(%v, %q) returned error: %v", c.got, c.min, err)
		}
		if got != c.want {
			t.Errorf("versionAtLeast(%v, %q) = %v, want %v", c.got, c.min, got, c.want)
		}
	}
}

func TestVersionAtLeast_InvalidMinimum(t *testing.T) {
	if _, err := versionAtLeast([]string{"1", "0", "0"}, "not-a-version"); err == nil {
		t.Error("versionAtLeast with a malformed minimum: got nil error, want non-nil")
	}
}

func TestPreflight_RunnerErrorNamesInstallHint(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux-operator": {Err: errors.New("exec: \"flux-operator\": executable file not found in $PATH")},
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("v3.16.2\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.34.2\n")},
	}}

	report := Preflight(context.Background(), f)
	var got Status
	for _, s := range report.Statuses {
		if s.Binary.Name == "flux-operator" {
			got = s
		}
	}
	if got.Found {
		t.Error("flux-operator: Found = true, want false")
	}
	if !strings.Contains(got.Problem, got.Binary.InstallHint) {
		t.Errorf("Problem = %q, want it to include the install hint %q", got.Problem, got.Binary.InstallHint)
	}
}
