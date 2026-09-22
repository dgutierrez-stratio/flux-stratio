// Command flux-stratio is a Flux CLI plugin (see fluxcd/flux2 RFC-0013) that
// migrates Stratio applications from an Ansible-based deployment onto a
// Flux/GitOps tenant: importing a tenant's live state, backing up and
// diffing each application against its rendered GitOps desired state, and
// patching it into the tenant's ResourceSetInputProvider.
package main

import (
	"fmt"
	"os"

	"github.com/Stratio/flux-stratio/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
