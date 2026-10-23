package prepare

import (
	"context"

	"github.com/Stratio/flux-stratio/internal/vault"
)

// litellmPasswordMoves are the legacy genai-litellm passwords its chart
// declares under hyphenated names (charts/litellm values.yaml,
// secretsBundle.litellm.passwords; same lengths as the legacy descriptor's
// randomString calls). db-salt-key encrypts the provider credentials and
// models LiteLLM stores in its database; keos-key-secret derives the API
// keys of every user and service registered with it (genai-api's among
// them); master-api-key is the admin key. keystore needs no move: it has
// the same name and length in both.
var litellmPasswordMoves = []PasswordMove{
	{From: "master_api_key", To: "master-api-key", Length: 13},
	{From: "db_salt_key", To: "db-salt-key", Length: 32},
	{From: "keos_key_secret", To: "keos-key-secret", Length: 32},
}

// stepLitellm moves the legacy LiteLLM passwords to the names the chart's
// SecretsBundle declares, so the release keeps the legacy values and the
// database it keeps stays readable, then removes the legacy Deployment
// (the chart changed its immutable selector; its legacy ingress, on
// genai.<domain>, sits on another host than the chart's and stays, as in
// replaceLegacyDeployment). The step never touches the legacy entries: the
// bundle removes them once the chart is applied, and a verbatim copy stays
// under legacy/.
//
// apps migrate runs it on every run, so it converges: the password moves
// skip what already holds (planPasswordMoves), and once the chart has
// replaced the legacy app (releaseCutOver) the step doesn't reach Vault at
// all and plans nothing.
var stepLitellm = Step{
	Name:              "prepare-litellm",
	Description:       "move the legacy LiteLLM Vault passwords to the chart's names and remove the legacy LiteLLM Deployment (the chart changed its immutable selector)",
	Automated:         true,
	ReplacesWorkloads: true,
	PasswordMoves:     litellmPasswordMoves,
	Plan: func(ctx context.Context, opts Options) ([]Operation, error) {
		deletes, err := planDeleteLegacy(ctx, opts, gvkDeployment)
		if err != nil {
			return nil, err
		}
		cutOver, err := releaseCutOver(ctx, opts, deletes)
		if err != nil || cutOver {
			return nil, err
		}
		v, err := vault.Connect(ctx, opts.Client, opts.Execer)
		if err != nil {
			return nil, err
		}
		moves, err := planPasswordMoves(ctx, v, opts, litellmPasswordMoves, opts.Log)
		if err != nil {
			return nil, err
		}
		return append(moves, deletes...), nil
	},
}
