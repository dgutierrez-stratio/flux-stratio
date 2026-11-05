package log

import (
	"bytes"
	"testing"
)

func TestVerbs(t *testing.T) {
	cases := []struct {
		name string
		call func(l *Logger)
		want string
	}{
		{"Actionf", func(l *Logger) { l.Actionf("doing %s", "x") }, "► doing x\n"},
		{"Generatef", func(l *Logger) { l.Generatef("wrote %s", "x") }, "✚ wrote x\n"},
		{"Waitingf", func(l *Logger) { l.Waitingf("waiting for %s", "x") }, "◎ waiting for x\n"},
		{"Successf", func(l *Logger) { l.Successf("%s done", "x") }, "✔ x done\n"},
		{"Warningf", func(l *Logger) { l.Warningf("careful: %s", "x") }, "⚠️ careful: x\n"},
		{"Failuref", func(l *Logger) { l.Failuref("%s failed", "x") }, "✗ x failed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.call(New(&buf, false))
			if got := buf.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithStepPrefixesEveryVerb(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, false).WithStep("remove-vpa")
	l.Actionf("uninstalling legacy VPA")
	l.Successf("legacy VPA uninstalled")

	want := "► [remove-vpa] uninstalling legacy VPA\n✔ [remove-vpa] legacy VPA uninstalled\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDebugfSilentUnlessVerbose(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, false).Debugf("chart loaded from %s", "cache")
	if buf.Len() != 0 {
		t.Errorf("expected no output when verbose=false, got %q", buf.String())
	}
}

func TestDebugfPrintsWhenVerbose(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, true).WithStep("bootstrap").Debugf("chart loaded from %s", "cache")
	want := "[bootstrap] chart loaded from cache\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Actionf("x")
	l.Generatef("x")
	l.Waitingf("x")
	l.Successf("x")
	l.Warningf("x")
	l.Failuref("x")
	l.Debugf("x")
	if got := l.WithStep("step"); got != nil {
		t.Errorf("expected WithStep on a nil Logger to return nil, got %v", got)
	}
}

func TestWithStepReturnsIndependentCopy(t *testing.T) {
	var buf bytes.Buffer
	base := New(&buf, false)
	tagged := base.WithStep("step-a")
	base.Actionf("untagged")
	tagged.Actionf("tagged")

	want := "► untagged\n► [step-a] tagged\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
