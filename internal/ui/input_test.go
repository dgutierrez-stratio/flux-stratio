package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLine_StopsAtEachNewline(t *testing.T) {
	in := strings.NewReader("first\nsecond\nlast")
	for _, want := range []string{"first", "second"} {
		got, err := ReadLine(in)
		if err != nil || got != want {
			t.Fatalf("ReadLine = %q, %v; want %q, nil", got, err, want)
		}
	}
	got, err := ReadLine(in)
	if got != "last" || !errors.Is(err, io.EOF) {
		t.Errorf("ReadLine at EOF = %q, %v; want \"last\", io.EOF", got, err)
	}
}
