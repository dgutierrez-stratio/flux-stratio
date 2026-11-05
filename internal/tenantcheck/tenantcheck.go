// Package tenantcheck validates a tenant's ResourceSetInputProvider file
// the way Flux will consume it: first as data (is it the right kind with
// the label the ResourceSets select on, does every component entry have a
// name, does every dependency point at an entry the file declares (an Error
// when it is `enabled: true`, a warning otherwise), is `enabled` a boolean), then by rendering every keos-use-cases ResourceSet
// against it with `flux-operator build rset` and checking that each one
// renders and that no rendered app waits on an `apps-*` Kustomization of
// the tenant that nothing renders (Flux would block it forever).
//
// It never talks to the cluster and never edits the file. Problems that
// make Flux misbehave are Errors; things worth a look are Warnings; a check
// that could not run (no flux-operator) is a Note, never a silent pass.
package tenantcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

const (
	rsipAPIVersion = "fluxcd.controlplane.io/v1"
	rsipKind       = "ResourceSetInputProvider"
	tenantLabel    = "keos.stratio.com/resourceset-type"
	tenantLabelVal = "tenant-config"
)

// Options configures a Validate run.
type Options struct {
	// Catalog is the parsed keos-use-cases (component keys, schemas).
	Catalog *catalog.Catalog
	// UseCases is the keos-use-cases checkout: its apps/components
	// resourceset-apps-*.yaml files are the ResourceSets rendered.
	UseCases string
	// Runner runs flux-operator. Nil skips the render step with a note.
	Runner runner.Runner
}

// Result is what a Validate run found.
type Result struct {
	// Errors make Flux misbehave: the run should fail.
	Errors []string
	// Warnings deserve a look but may be intended.
	Warnings []string
	// Notes say which check could not run.
	Notes []string
	// Rendered counts the Kustomizations the ResourceSets rendered.
	Rendered int
}

// OK reports whether nothing is wrong.
func (r Result) OK() bool { return len(r.Errors) == 0 }

type rsip struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Type          string `yaml:"type"`
		DefaultValues struct {
			TenantName string                      `yaml:"tenantName"`
			Size       string                      `yaml:"size"`
			Components map[string][]map[string]any `yaml:"components"`
		} `yaml:"defaultValues"`
	} `yaml:"spec"`
}

// Validate checks the tenant file at path. The error is for a file that
// cannot be read or parsed at all; everything else is in the Result.
func Validate(ctx context.Context, path string, opts Options) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var doc rsip
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Result{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	var r Result
	checkStatic(doc, opts, &r)
	renderAll(ctx, path, doc, opts, &r)
	return r, nil
}

func (r *Result) fail(format string, a ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
}
func (r *Result) warn(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

func checkStatic(doc rsip, opts Options, r *Result) {
	errorf, warnf := r.fail, r.warn

	if doc.APIVersion != rsipAPIVersion || doc.Kind != rsipKind {
		errorf("not a %s %s (found %s %s)", rsipAPIVersion, rsipKind, doc.APIVersion, doc.Kind)
	}
	if doc.Metadata.Name == "" {
		errorf("metadata.name is empty")
	}
	if doc.Metadata.Labels[tenantLabel] != tenantLabelVal {
		errorf("label %s: %s is missing, so no ResourceSet selects this file", tenantLabel, tenantLabelVal)
	}
	if doc.Spec.Type != "Static" {
		errorf("spec.type is %q, want Static", doc.Spec.Type)
	}
	dv := doc.Spec.DefaultValues
	switch {
	case dv.TenantName == "":
		errorf("spec.defaultValues.tenantName is empty")
	case doc.Metadata.Name != "" && dv.TenantName != doc.Metadata.Name:
		warnf("spec.defaultValues.tenantName %q differs from metadata.name %q", dv.TenantName, doc.Metadata.Name)
	}
	if !slices.Contains([]string{"S", "M", "L"}, dv.Size) {
		errorf("spec.defaultValues.size is %q, want S, M or L", dv.Size)
	}

	declared := map[string]map[string]bool{} // key -> entry names
	keys := make([]string, 0, len(dv.Components))
	for key := range dv.Components {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if opts.Catalog != nil {
			if _, ok := opts.Catalog.Schemas[key]; !ok {
				warnf("components.%s is not a component key any ResourceSet in keos-use-cases reads: it is ignored", key)
			}
		}
		declared[key] = map[string]bool{}
		for i, entry := range dv.Components[key] {
			name, _ := entry["name"].(string)
			if name == "" {
				errorf("components.%s[%d] has no name", key, i)
				continue
			}
			if declared[key][name] {
				errorf("components.%s: entry %q is declared twice", key, name)
			}
			declared[key][name] = true
		}
	}

	for _, key := range keys {
		for _, entry := range dv.Components[key] {
			name, _ := entry["name"].(string)
			if name == "" {
				continue
			}
			checkDependencies(fmt.Sprintf("components.%s[%s]", key, name), entry, declared, opts, r)
		}
	}
}

func checkDependencies(label string, entry map[string]any, declared map[string]map[string]bool, opts Options, r *Result) {
	config, _ := entry["config"].(map[string]any)
	deps, _ := config["dependencies"].(map[string]any)
	depKeys := make([]string, 0, len(deps))
	for k := range deps {
		depKeys = append(depKeys, k)
	}
	sort.Strings(depKeys)
	for _, dk := range depKeys {
		dep, ok := deps[dk].(map[string]any)
		if !ok {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: config.dependencies.%s is not a mapping", label, dk))
			continue
		}
		if en, present := dep["enabled"]; present {
			if _, isBool := en.(bool); !isBool {
				r.Errors = append(r.Errors, fmt.Sprintf("%s: config.dependencies.%s.enabled is %#v, want true or false (a string is truthy in the templates)", label, dk, en))
			}
		}
		// A dependency only blocks the app when it is switched on: the
		// ResourceSets gate several (genai, virtualizer, ...) on `enabled`,
		// default false, and render no dependsOn for a disabled one. Whether
		// an ungated dependency without `enabled` is waited on is what the
		// render check decides, so here only an explicit `enabled: true` is
		// certain enough to be an Error.
		on, _ := dep["enabled"].(bool)
		problem := r.warn
		if on {
			problem = r.fail
		}
		name, _ := dep["name"].(string)
		if name == "" {
			problem("%s: config.dependencies.%s.name is empty; fill it in by hand%s", label, dk, unlessEnabled(on))
			continue
		}
		// A dependency key names the component key it points at; one that is
		// not a component key (governancePostgres) cannot be checked here.
		if opts.Catalog == nil {
			continue
		}
		if _, isComponent := opts.Catalog.Schemas[dk]; !isComponent {
			continue
		}
		if !declared[dk][name] {
			problem("%s: config.dependencies.%s points at %q, but components.%s declares %s%s",
				label, dk, name, dk, declaredList(declared[dk]), unwaited(on, name))
		}
	}
}

// unlessEnabled and unwaited complete a dependency message with what it
// means for the app, which depends on whether the dependency is switched on.
func unlessEnabled(on bool) string {
	if on {
		return ""
	}
	return " (not enabled: nothing waits on it if its ResourceSet gates it on `enabled`)"
}

func unwaited(on bool, name string) string {
	if on {
		return ": the app would wait on apps-" + name + " forever"
	}
	return " (not enabled: nothing waits on it if its ResourceSet gates it on `enabled`; the render check below says)"
}

func declaredList(names map[string]bool) string {
	if len(names) == 0 {
		return "none"
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// renderAll renders every ResourceSet of keos-use-cases against the file.
func renderAll(ctx context.Context, path string, doc rsip, opts Options, r *Result) {
	if opts.Runner == nil || opts.UseCases == "" {
		r.Notes = append(r.Notes, "render check skipped: no keos-use-cases checkout or runner")
		return
	}
	files, err := filepath.Glob(filepath.Join(opts.UseCases, "apps", "components", "resourceset-apps-*.yaml"))
	if err != nil || len(files) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("render check skipped: no resourceset-apps-*.yaml under %s/apps/components", opts.UseCases))
		return
	}
	sort.Strings(files)

	type ks struct{ name, namespace string }
	rendered := map[ks]bool{}
	type wait struct {
		by string
		on ks
	}
	var waits []wait
	for _, f := range files {
		stdout, stderr, err := opts.Runner.Run(ctx, "flux-operator", "build", "rset", "-f", f, "--inputs-from-provider", path)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				r.Notes = append(r.Notes, "render check skipped: flux-operator is not installed")
				return
			}
			r.Errors = append(r.Errors, fmt.Sprintf("%s does not render with this file: %s", filepath.Base(f), firstLines(string(stderr), err)))
			continue
		}
		docs, err := yamldocs.Decode(stdout)
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: unreadable output of flux-operator build rset: %v", filepath.Base(f), err))
			continue
		}
		for _, k := range yamldocs.FindByKind(docs, "Kustomization") {
			self := ks{k.GetName(), k.GetNamespace()}
			rendered[self] = true
			r.Rendered++
			for _, d := range dependsOn(k) {
				waits = append(waits, wait{by: k.GetName(), on: ks{d.name, d.namespace}})
			}
		}
	}
	if r.Rendered == 0 {
		r.Errors = append(r.Errors, "no ResourceSet renders a Kustomization for this file")
		return
	}

	// Only `apps-*` Kustomizations of this tenant come from these
	// ResourceSets; the rest (default-secrets, system-services-*) are
	// rendered by other repositories and cannot be judged here.
	tenant := doc.Spec.DefaultValues.TenantName
	seen := map[string]bool{}
	for _, w := range waits {
		if rendered[w.on] || !strings.HasPrefix(w.on.name, "apps-") || !inTenant(w.on.namespace, tenant) {
			continue
		}
		msg := fmt.Sprintf("%s waits on %s/%s, which no ResourceSet renders from this file (a dependency or component entry is missing); Flux would block it forever", w.by, w.on.namespace, w.on.name)
		if !seen[msg] {
			seen[msg] = true
			r.Errors = append(r.Errors, msg)
		}
	}
}

type depRef struct{ name, namespace string }

func dependsOn(k *unstructured.Unstructured) []depRef {
	list, _, _ := unstructured.NestedSlice(k.Object, "spec", "dependsOn")
	var out []depRef
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = k.GetNamespace()
		}
		out = append(out, depRef{name, ns})
	}
	return out
}

func inTenant(namespace, tenant string) bool {
	return tenant != "" && (namespace == tenant || strings.HasPrefix(namespace, tenant+"-"))
}

// firstLines is the part of flux-operator's stderr worth showing: its first
// non-empty lines, or the exec error when it printed nothing.
func firstLines(stderr string, err error) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
		if len(lines) == 3 {
			break
		}
	}
	if len(lines) == 0 {
		return err.Error()
	}
	return strings.Join(lines, " | ")
}
