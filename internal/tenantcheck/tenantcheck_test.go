package tenantcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/runner"
)

const goodTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
  namespace: flux-system
  labels:
    keos.stratio.com/resourceset-type: tenant-config
spec:
  type: Static
  defaultValues:
    tenantName: stratio
    size: S
    components:
      postgres:
      - name: psql
      virtualizer:
      - name: virtualizer
        config:
          dependencies:
            postgres:
              enabled: true
              name: psql
            governancePostgres:
              name: anything
`

func fixtureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load("../tenantimport/testdata/catalog")
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func kustomization(name, namespace string, dependsOn ...string) string {
	deps := ""
	for _, d := range dependsOn {
		ns, n, _ := strings.Cut(d, "/")
		deps += fmt.Sprintf("  - name: %s\n    namespace: %s\n", n, ns)
	}
	if deps != "" {
		deps = "  dependsOn:\n" + deps
	}
	return fmt.Sprintf("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n%s", name, namespace, deps)
}

func check(t *testing.T, tenant string, resp runner.FakeResponse) Result {
	t.Helper()
	dir := t.TempDir()
	uc := filepath.Join(dir, "uc")
	if err := os.MkdirAll(filepath.Join(uc, "apps", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uc, "apps", "components", "resourceset-apps-x.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tenant.yaml")
	if err := os.WriteFile(path, []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Validate(context.Background(), path, Options{
		Catalog: fixtureCatalog(t), UseCases: uc,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{"flux-operator": resp}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rendersOK() runner.FakeResponse {
	return runner.FakeResponse{Stdout: []byte(
		kustomization("apps-psql", "stratio-datastores", "keos-datastores/system-services-postgres-operator") + "---\n" +
			kustomization("apps-virtualizer", "stratio-apps", "stratio-datastores/apps-psql"))}
}

func has(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestValidate_GoodFileIsOK(t *testing.T) {
	r := check(t, goodTenant, rendersOK())
	if !r.OK() || len(r.Warnings) != 0 || len(r.Notes) != 0 || r.Rendered != 2 {
		t.Errorf("got errors=%v warnings=%v notes=%v rendered=%d, want a clean pass of 2 Kustomizations", r.Errors, r.Warnings, r.Notes, r.Rendered)
	}
}

func TestValidate_StaticProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(string) string
		want   string
		isWarn bool
	}{
		"missing label": {func(s string) string {
			return strings.Replace(s, "    keos.stratio.com/resourceset-type: tenant-config\n", "    other: x\n", 1)
		}, "no ResourceSet selects", false},
		"wrong kind": {func(s string) string {
			return strings.Replace(s, "kind: ResourceSetInputProvider", "kind: ConfigMap", 1)
		}, "not a", false},
		"bad size":           {func(s string) string { return strings.Replace(s, "size: S", "size: XL", 1) }, "size", false},
		"empty tenantName":   {func(s string) string { return strings.Replace(s, "tenantName: stratio", `tenantName: ""`, 1) }, "tenantName is empty", false},
		"tenantName differs": {func(s string) string { return strings.Replace(s, "tenantName: stratio", "tenantName: other", 1) }, "differs from metadata.name", true},
		"unknown component": {func(s string) string {
			return strings.Replace(s, "      postgres:\n", "      nonsense:\n      - name: n\n      postgres:\n", 1)
		}, "components.nonsense", true},
		"entry without name": {func(s string) string { return strings.Replace(s, "      - name: psql\n", "      - config: {}\n", 1) }, "components.postgres[0] has no name", false},
		"duplicate entry": {func(s string) string {
			return strings.Replace(s, "      - name: psql\n", "      - name: psql\n      - name: psql\n", 1)
		}, "declared twice", false},
		"enabled as string": {func(s string) string { return strings.Replace(s, "enabled: true", `enabled: "true"`, 1) }, "want true or false", false},
		"empty dependency name": {func(s string) string {
			return strings.Replace(s, "              name: psql\n", `              name: ""`+"\n", 1)
		}, "name is empty", false},
		"undeclared dependency": {func(s string) string {
			return strings.Replace(s, "              name: psql\n", "              name: other\n", 1)
		}, `points at "other"`, false},
	} {
		r := check(t, tc.mutate(goodTenant), rendersOK())
		list := r.Errors
		if tc.isWarn {
			list = r.Warnings
		}
		if !has(list, tc.want) {
			t.Errorf("%s: errors=%v warnings=%v, want one containing %q", name, r.Errors, r.Warnings, tc.want)
		}
		if tc.isWarn && !r.OK() {
			t.Errorf("%s: a warning made the run fail: %v", name, r.Errors)
		}
	}
}

func TestValidate_RenderFailureIsAnErrorWithTheCause(t *testing.T) {
	r := check(t, goodTenant, runner.FakeResponse{
		Stderr: []byte("\nerror: template: x:12: function \"nope\" not defined\n"),
		Err:    errors.New("exit status 1"),
	})
	if !has(r.Errors, "resourceset-apps-x.yaml does not render") || !has(r.Errors, `function "nope" not defined`) {
		t.Errorf("Errors = %v, want the rset and flux-operator's reason", r.Errors)
	}
}

func TestValidate_DependsOnAnAppNobodyRendersIsAnError(t *testing.T) {
	out := kustomization("apps-virtualizer", "stratio-apps",
		"stratio-datastores/apps-psql",                      // an app of the tenant nothing renders
		"keos-datastores/system-services-postgres-operator", // other repository: not judged
		"stratio-system/default-secrets-stratio")            // not apps-*: not judged
	r := check(t, goodTenant, runner.FakeResponse{Stdout: []byte(out)})
	if len(r.Errors) != 1 || !has(r.Errors, "waits on stratio-datastores/apps-psql") {
		t.Errorf("Errors = %v, want exactly the missing apps-psql", r.Errors)
	}
}

func TestValidate_NothingRenderedIsAnError(t *testing.T) {
	r := check(t, goodTenant, runner.FakeResponse{Stdout: []byte("")})
	if !has(r.Errors, "no ResourceSet renders") {
		t.Errorf("Errors = %v", r.Errors)
	}
}

func TestValidate_MissingFluxOperatorIsANoteNotAPass(t *testing.T) {
	r := check(t, goodTenant, runner.FakeResponse{Err: fmt.Errorf("running x: %w", exec.ErrNotFound)})
	if !r.OK() || !has(r.Notes, "flux-operator is not installed") {
		t.Errorf("errors=%v notes=%v, want the static checks to pass and a note about the skipped render", r.Errors, r.Notes)
	}
}

func TestValidate_UnparsableFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(path, []byte("a: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), path, Options{}); err == nil {
		t.Error("an unparsable file validated")
	}
}

func TestValidate_ADisabledDependencyOnlyWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(string) string
		want   string
	}{
		"empty name, enabled false": {func(s string) string {
			return strings.Replace(s, "              enabled: true\n              name: psql\n", "              enabled: false\n              name: \"\"\n", 1)
		}, "name is empty"},
		"empty name, no enabled": {func(s string) string {
			return strings.Replace(s, "              enabled: true\n              name: psql\n", "              name: \"\"\n", 1)
		}, "name is empty"},
		"undeclared, enabled false": {func(s string) string {
			return strings.Replace(s, "              enabled: true\n              name: psql\n", "              enabled: false\n              name: other\n", 1)
		}, `points at "other"`},
		"undeclared, no enabled key": {func(s string) string {
			return strings.Replace(s, "              enabled: true\n              name: psql\n", "              name: other\n", 1)
		}, `points at "other"`},
	} {
		r := check(t, tc.mutate(goodTenant), rendersOK())
		if !r.OK() {
			t.Errorf("%s: Errors = %v, want none: a dependency that is not enabled blocks nothing", name, r.Errors)
		}
		if !has(r.Warnings, tc.want) || !has(r.Warnings, "not enabled") {
			t.Errorf("%s: Warnings = %v, want one containing %q saying it is not enabled", name, r.Warnings, tc.want)
		}
	}
}

func TestValidate_AFreshImportSkeletonPassesWhenUnresolvedDependenciesAreOff(t *testing.T) {
	// What a first import writes for a dependency it could not find: a name
	// left empty and no `enabled`. Not an error by itself; the render check
	// is what fails when something really waits on it.
	skeleton := strings.Replace(goodTenant, "              enabled: true\n              name: psql\n", "              name: \"\"\n", 1)
	if r := check(t, skeleton, rendersOK()); !r.OK() {
		t.Errorf("Errors = %v, want a pass", r.Errors)
	}
	// ... and when it does, the render check says so.
	waiting := kustomization("apps-virtualizer", "stratio-apps", "stratio-datastores/apps-")
	if r := check(t, skeleton, runner.FakeResponse{Stdout: []byte(waiting)}); r.OK() || !has(r.Errors, "waits on stratio-datastores/apps-") {
		t.Errorf("Errors = %v, want the render check to fail on the dangling wait", r.Errors)
	}
}
