package main

import (
	"io"
	"testing"
)

func TestRunRejectsAnUnknownFlag(t *testing.T) {
	if err := run([]string{"-nope"}, io.Discard); err == nil {
		t.Error("run() accepted a flag it does not define")
	}
}
