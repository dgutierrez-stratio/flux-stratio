// Command flux-stratio is a Flux CLI plugin (see fluxcd/flux2 RFC-0013) that
// migrates Stratio applications from an Ansible-based deployment onto a
// Flux/GitOps tenant: importing a tenant's live state, backing up and
// diffing each application against its rendered GitOps desired state, and
// patching it into the tenant's ResourceSetInputProvider.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Stratio/flux-stratio/internal/cli"
)

func main() {
	// Ctrl-C or SIGTERM cancels the command's context, so a cluster call
	// or a subprocess in flight stops cleanly and the command returns its
	// error, rather than the process dying mid-operation. Only the first
	// signal is taken: a command blocked where no context reaches (an
	// operator prompt waiting on stdin) would otherwise ignore every further
	// Ctrl-C and never exit, so the default behaviour comes back after it
	// and a second one quits immediately.
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-sigs; !ok {
			return
		}
		fmt.Fprintln(os.Stderr, "interrupted; stopping — press Ctrl-C again to quit immediately")
		cancel()
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	}()
	err := cli.NewRootCommand().ExecuteContext(ctx)
	signal.Stop(sigs)
	close(sigs)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
