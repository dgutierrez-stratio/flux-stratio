package cli

import (
	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/doctor"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/runner"
)

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that flux-stratio is ready to run: binaries, catalog, environment, repo layout, cluster access, tenant file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := doctor.Run(cmd.Context(), doctor.Options{
				ConfigFlag:     configFlag,
				EnvConfigFlag:  envConfigFlag,
				Overrides:      envOverrides(),
				KubeconfigArgs: kubeconfigArgs,
				Runner:         runner.Exec{},
				NewClient:      kubeclient.New,
				Log:            rootLogger(cmd),
			})
			return report.Err()
		},
	}
}
