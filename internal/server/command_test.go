package server

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestServerSubcommandHelp(t *testing.T) {
	for _, arguments := range [][]string{{"init", "--help"}, {"cert", "status", "--help"}} {
		var output bytes.Buffer
		if err := Command(context.Background(), arguments, &output, io.Discard); err != nil {
			t.Fatalf("Command(%v): %v", arguments, err)
		}
		if !strings.Contains(output.String(), "server init") || !strings.Contains(output.String(), "lan-managed") {
			t.Fatalf("help output = %q", output.String())
		}
	}
}
