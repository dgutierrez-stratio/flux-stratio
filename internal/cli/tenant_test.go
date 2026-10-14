package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantimport"
)

const tenantFileWithoutUIEnabled = `# hand-written
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: true
    internalS3BucketName: ""
    userEmail: me@stratio.com
    userId: me
    userName: me
    components:
      virtualizer:
      - name: virtualizer
        config:
          dependencies:
            postgres:
              name: psql
            pgbouncer:
              name: pool
      # kafka:
      # - name: kafka1
`

func tenantImportFixture(t *testing.T) (tenantimport.Options, string) {
	t.Helper()
	cat, err := catalog.Load(filepath.Join("..", "tenantimport", "testdata", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	scheme := apiruntime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dep := func(name string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "stratio-apps"}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep("virtualizer"), dep("virtualizer-ui")).Build()
	seed := config.SeedCatalog()

	path := filepath.Join(t.TempDir(), "stratio.yaml")
	if err := os.WriteFile(path, []byte(tenantFileWithoutUIEnabled), 0o644); err != nil {
		t.Fatal(err)
	}
	return tenantimport.Options{TenantName: "stratio", Catalog: cat, Config: &seed, Client: c}, path
}

func runComplete(t *testing.T, opts tenantimport.Options, f tenantImportFlags) string {
	t.Helper()
	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	if err := completeTenantFile(cmd, opts, f, log.New(io.Discard, false), func([]byte, string) error { return nil }); err != nil {
		t.Fatalf("completeTenantFile: %v", err)
	}
	return stdout.String()
}

func TestTenantImport_DryRunPrintsTheAdditionsAndWritesNothing(t *testing.T) {
	opts, path := tenantImportFixture(t)
	out := runComplete(t, opts, tenantImportFlags{output: path, dryRun: true})

	if !strings.HasPrefix(out, "--- "+tenantFileCurrent+"\n+++ "+tenantFileWithNew+"\n") {
		t.Errorf("the diff doesn't name its sides:\n%s", out)
	}
	if !strings.Contains(out, "+          uiEnabled: true") {
		t.Errorf("the diff lacks the uiEnabled addition:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- ") { // "--- " names the old side
			t.Errorf("the dry-run diff removes a line: %q", line)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != tenantFileWithoutUIEnabled {
		t.Errorf("--dry-run wrote the file:\n%s", data)
	}
}

func TestTenantImport_CompleteWritesOnlyAdditionsAndIsIdempotent(t *testing.T) {
	opts, path := tenantImportFixture(t)
	runComplete(t, opts, tenantImportFlags{output: path})

	got, _ := os.ReadFile(path)
	want := strings.Replace(tenantFileWithoutUIEnabled, "              name: pool\n", "              name: pool\n          uiEnabled: true\n", 1)
	if string(got) != want {
		t.Errorf("completed file:\n%s\nwant:\n%s", got, want)
	}

	info, _ := os.Stat(path)
	runComplete(t, opts, tenantImportFlags{output: path})
	again, _ := os.ReadFile(path)
	if string(again) != string(got) {
		t.Errorf("a second run changed the file:\n%s", again)
	}
	if info2, _ := os.Stat(path); !info2.ModTime().Equal(info.ModTime()) {
		t.Error("a second run rewrote the file (modification time changed)")
	}
}

func TestTenantImport_DryRunNeedsAnExistingFile(t *testing.T) {
	for _, f := range []tenantImportFlags{
		{output: filepath.Join(t.TempDir(), "absent.yaml"), dryRun: true},
		{dryRun: true},
	} {
		cmd := &cobra.Command{}
		cmd.SetErr(io.Discard)
		if err := runTenantImport(cmd, f); err == nil || !strings.Contains(err.Error(), "only applies when completing") {
			t.Errorf("flags %+v: error = %v, want 'only applies when completing an existing --output file'", f, err)
		}
	}
}

func TestTenantImport_ValidationFailureFailsTheCommandButTheFileIsWritten(t *testing.T) {
	opts, path := tenantImportFixture(t)
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	boom := errors.New("the tenant file has 1 problem(s)")
	err := completeTenantFile(cmd, opts, tenantImportFlags{output: path}, log.New(io.Discard, false),
		func([]byte, string) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the validation error", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "uiEnabled: true") {
		t.Errorf("the additions were not written before validating:\n%s", data)
	}
}
