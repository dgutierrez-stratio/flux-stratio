package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readingFakeRunner reads its two file-path arguments immediately (as the
// real meld process would, before Meld's caller can clean them up) and
// records their content for the test to assert on afterward.
type readingFakeRunner struct {
	calledWith    []string
	leftContents  string
	rightContents string
	err           error
}

func (r *readingFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	r.calledWith = append([]string{name}, args...)
	if r.err != nil {
		return nil, nil, r.err
	}
	left, err := os.ReadFile(args[0])
	if err != nil {
		return nil, nil, err
	}
	right, err := os.ReadFile(args[1])
	if err != nil {
		return nil, nil, err
	}
	r.leftContents, r.rightContents = string(left), string(right)
	return nil, nil, nil
}

func TestMeld_WritesTempFilesAndRunsMeld(t *testing.T) {
	r := &readingFakeRunner{}
	if err := Meld(context.Background(), r, "desired", "desired-content\n", "live", "live-content\n"); err != nil {
		t.Fatalf("Meld returned error: %v", err)
	}

	if len(r.calledWith) != 3 || r.calledWith[0] != "meld" {
		t.Fatalf("calledWith = %v, want [meld <left> <right>]", r.calledWith)
	}
	if r.leftContents != "desired-content\n" {
		t.Errorf("left file content = %q", r.leftContents)
	}
	if r.rightContents != "live-content\n" {
		t.Errorf("right file content = %q", r.rightContents)
	}

	leftFile, rightFile := r.calledWith[1], r.calledWith[2]
	if !strings.HasPrefix(filepath.Base(leftFile), "desired-") {
		t.Errorf("left temp file = %q, want it named after the desired label", leftFile)
	}
	if !strings.HasPrefix(filepath.Base(rightFile), "live-") {
		t.Errorf("right temp file = %q, want it named after the live label", rightFile)
	}
	if _, err := os.Stat(leftFile); !os.IsNotExist(err) {
		t.Errorf("left temp file was not removed after Meld returned")
	}
	if _, err := os.Stat(rightFile); !os.IsNotExist(err) {
		t.Errorf("right temp file was not removed after Meld returned")
	}
}

func TestMeld_RunnerErrorPropagates(t *testing.T) {
	r := &readingFakeRunner{err: context.DeadlineExceeded}
	if err := Meld(context.Background(), r, "left", "a", "right", "b"); err == nil {
		t.Fatal("Meld with a failing runner: got nil error, want non-nil")
	}
}

// tabsRunner records one meld invocation made of "--diff <left> <right>"
// triples, reading every file while the files still exist (as meld would
// before MeldTabs cleans up) and remembering where they were.
type tabsRunner struct {
	calls              int
	name               string
	args               []string
	lefts, rights      []string // contents
	leftPaths, rtPaths []string
	err                error
}

func (r *tabsRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	r.calls++
	r.name, r.args = name, args
	if r.err != nil {
		return nil, nil, r.err
	}
	for i := 0; i < len(args); i += 3 {
		if args[i] != "--diff" || i+2 >= len(args) {
			return nil, nil, os.ErrInvalid
		}
		l, err := os.ReadFile(args[i+1])
		if err != nil {
			return nil, nil, err
		}
		rt, err := os.ReadFile(args[i+2])
		if err != nil {
			return nil, nil, err
		}
		r.lefts, r.rights = append(r.lefts, string(l)), append(r.rights, string(rt))
		r.leftPaths, r.rtPaths = append(r.leftPaths, args[i+1]), append(r.rtPaths, args[i+2])
	}
	return nil, nil, nil
}

// TestMeldTabs_OneMeldAndATabPerResource: a single meld process gets every
// comparison as its own --diff, and every file is named "<label>-<resource>"
// — the label says which side it is, the resource what is compared — so the
// tab and the panes in meld identify both, as the single comparison's files
// always did.
func TestMeldTabs_OneMeldAndATabPerResource(t *testing.T) {
	r := &tabsRunner{}
	items := []Comparison{
		{Name: "env dg-hdfs-agent", Left: "A=1\n", Right: "A=2\n"},
		{Name: "spec Deployment/dg-hdfs-agent", Left: "replicas: 1\n", Right: "replicas: 2\n"},
	}
	if err := MeldTabs(context.Background(), r, "backup", "live now", items); err != nil {
		t.Fatalf("MeldTabs: %v", err)
	}
	if r.calls != 1 || r.name != "meld" {
		t.Fatalf("meld ran %d time(s) as %q, want once", r.calls, r.name)
	}
	if len(r.args) != 6 || r.args[0] != "--diff" || r.args[3] != "--diff" {
		t.Fatalf("args = %v, want two --diff <left> <right>", r.args)
	}
	if !reflect.DeepEqual(r.lefts, []string{"A=1\n", "replicas: 1\n"}) || !reflect.DeepEqual(r.rights, []string{"A=2\n", "replicas: 2\n"}) {
		t.Errorf("contents: left %q right %q", r.lefts, r.rights)
	}
	for i, resource := range []string{"env dg-hdfs-agent", "spec Deployment-dg-hdfs-agent"} {
		if got, want := filepath.Base(r.leftPaths[i]), "backup-"+resource+".yaml"; got != want {
			t.Errorf("tab %d: left file %q, want %q", i, got, want)
		}
		if got, want := filepath.Base(r.rtPaths[i]), "live now-"+resource+".yaml"; got != want {
			t.Errorf("tab %d: right file %q, want %q", i, got, want)
		}
	}
	for _, p := range append(append([]string{}, r.leftPaths...), r.rtPaths...) {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was left behind after meld closed", p)
		}
	}
}

// TestMeldTabs_EverySideIsNamedAfterItsLabel: whatever the tab holds, the
// left file starts with the left label and the right one with the right
// label, and the two names differ — so a tab can't be read the wrong way
// round or lose which side is which.
func TestMeldTabs_EverySideIsNamedAfterItsLabel(t *testing.T) {
	r := &tabsRunner{}
	items := []Comparison{
		{Env: true, Name: "env web", Left: "1", Right: "2"},
		{Env: true, Name: "env web (identical)", Left: "3", Right: "3"},
		{Name: "spec Deployment/web", Left: "4", Right: "5"},
		{Name: "", Left: "6", Right: "7"},
	}
	if err := MeldTabs(context.Background(), r, "desired state", "live cluster", items); err != nil {
		t.Fatalf("MeldTabs: %v", err)
	}
	if len(r.leftPaths) != len(items) {
		t.Fatalf("tabs = %d, want %d", len(r.leftPaths), len(items))
	}
	for i := range items {
		l, rt := filepath.Base(r.leftPaths[i]), filepath.Base(r.rtPaths[i])
		if !strings.HasPrefix(l, "desired state-") || !strings.HasPrefix(rt, "live cluster-") || l == rt {
			t.Errorf("tab %d: files %q and %q, want them named after their labels", i, l, rt)
		}
		if !strings.HasSuffix(l, ".yaml") || !strings.HasSuffix(rt, ".yaml") {
			t.Errorf("tab %d: files %q and %q should be .yaml", i, l, rt)
		}
	}
}

// TestMeldTabs_UniqueFileNames: two resources of one name, or one that
// would climb out of the directory or read as a meld option, still get a
// file of their own inside it, and equal labels still tell two sides apart.
func TestMeldTabs_UniqueFileNames(t *testing.T) {
	r := &tabsRunner{}
	items := []Comparison{
		{Name: "env web", Left: "1", Right: "2"},
		{Name: "env web", Left: "3", Right: "4"},
		{Name: "../../etc/x", Left: "5", Right: "6"},
		{Name: "--version", Left: "7", Right: "8"},
		{Name: "", Left: "9", Right: "0"},
	}
	if err := MeldTabs(context.Background(), r, "same", "same", items); err != nil {
		t.Fatalf("MeldTabs: %v", err)
	}
	seen := map[string]bool{}
	for i := range r.leftPaths {
		for _, p := range []string{r.leftPaths[i], r.rtPaths[i]} {
			if seen[p] {
				t.Errorf("tab %d reuses %s", i, p)
			}
			seen[p] = true
			if strings.HasPrefix(filepath.Base(p), "-") {
				t.Errorf("tab %d: %s would read as a meld option", i, p)
			}
			if filepath.Dir(p) != filepath.Dir(r.leftPaths[0]) {
				t.Errorf("tab %d: %s escaped the temporary directory", i, p)
			}
		}
	}
	if len(r.leftPaths) != 5 || r.lefts[1] != "3" || r.rights[1] != "4" {
		t.Errorf("tabs = %d, second left/right = %q/%q", len(r.leftPaths), r.lefts[1], r.rights[1])
	}
}

func TestMeldTabs_NothingToOpen_RunsNothing(t *testing.T) {
	r := &tabsRunner{}
	if err := MeldTabs(context.Background(), r, "a", "b", nil); err != nil || r.calls != 0 {
		t.Errorf("err = %v, calls = %d; want nothing run", err, r.calls)
	}
}

func TestMeldTabs_ErrorsNameMeld(t *testing.T) {
	r := &tabsRunner{err: errors.New("exec: meld: not found")}
	err := MeldTabs(context.Background(), r, "a", "b", []Comparison{{Name: "x", Left: "1", Right: "2"}})
	if err == nil || !strings.Contains(err.Error(), "running meld") {
		t.Errorf("err = %v, want it to say meld failed", err)
	}
}

// TestTabsToOpen: every resource gets a tab — an absent one would read as a
// comparison that was lost. Environment variables come first and everything
// else after; within each group the differing ones first and the identical
// ones last, marked; and nothing is changed in what the tabs hold.
func TestTabsToOpen(t *testing.T) {
	envSame := Comparison{Env: true, Name: "env same", Left: "x", Right: "x"}
	envDiff := Comparison{Env: true, Name: "env diff", Left: "a", Right: "b"}
	specSame := Comparison{Name: "spec same", Left: "", Right: ""}
	specDiff1 := Comparison{Name: "spec d1", Left: "a", Right: "b"}
	specDiff2 := Comparison{Name: "spec d2", Left: "", Right: "c"}
	got := TabsToOpen([]Comparison{specDiff1, specSame, envSame, specDiff2, envDiff})
	want := []Comparison{
		envDiff, {Env: true, Name: "env same (identical)", Left: "x", Right: "x"},
		specDiff1, specDiff2, {Name: "spec same (identical)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if envSame.Name != "env same" {
		t.Errorf("the input was modified: %+v", envSame)
	}
	if got := TabsToOpen(nil); len(got) != 0 {
		t.Errorf("nothing: got %v", got)
	}
}

// TestOpenMeld: several resources are tabs of one window — all of them,
// identical ones included; one resource, or none, is the single comparison
// it always was.
func TestOpenMeld(t *testing.T) {
	items := []Comparison{
		{Name: "spec s", Left: "0", Right: "9"}, {Env: true, Name: "env a", Left: "1", Right: "2"},
		{Env: true, Name: "env b", Left: "3", Right: "4"}, {Env: true, Name: "env c", Left: "5", Right: "5"},
	}
	r := &tabsRunner{}
	if err := OpenMeld(context.Background(), r, "backup", "L", "live now", "R", items); err != nil {
		t.Fatalf("OpenMeld: %v", err)
	}
	if r.calls != 1 || len(r.args) != 12 {
		t.Fatalf("args = %v, want four tabs: no resource may be left out", r.args)
	}
	var names []string
	for _, p := range r.leftPaths {
		names = append(names, filepath.Base(p))
	}
	if !reflect.DeepEqual(names, []string{"backup-env a.yaml", "backup-env b.yaml", "backup-env c (identical).yaml", "backup-spec s.yaml"}) {
		t.Errorf("tabs = %v, want the environment variables first (differing, then identical) and the spec after", names)
	}

	for _, one := range [][]Comparison{nil, items[1:2]} {
		single := &readingFakeRunner{}
		if err := OpenMeld(context.Background(), single, "backup", "L\n", "live now", "R\n", one); err != nil {
			t.Fatalf("OpenMeld: %v", err)
		}
		if len(single.calledWith) != 3 || single.leftContents != "L\n" || single.rightContents != "R\n" {
			t.Errorf("%d item(s): calledWith = %v, left %q right %q; want the one plain comparison of left and right", len(one), single.calledWith, single.leftContents, single.rightContents)
		}
	}
}
