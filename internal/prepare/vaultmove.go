package prepare

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/vault"
)

// KindVaultPassword is the Kind of a Vault operation's Object: a Vault
// entry, not a cluster object. Its namespace is the entry's Vault folder.
const KindVaultPassword = "VaultPassword"

// legacyBackupDir is the subfolder of an app's passwords folder that keeps
// a verbatim copy of every legacy entry a step moved: the chart's
// SecretsBundle deletes each entry it doesn't declare in its own folder,
// the legacy originals among them, but never a subfolder.
const legacyBackupDir = "legacy"

// passwordField is the field a SecretsBundle "generic" password keeps its
// value in (its keyName, "pass" in every chart).
const passwordField = "pass"

// gvkSecretsBundle is the secrets operator's bundle, which a chart's
// release creates under the release's name.
var gvkSecretsBundle = schema.GroupVersionKind{Group: "secrets.stratio.com", Version: "v2", Kind: "SecretsBundle"}

// PasswordMove is one legacy Vault password the chart's SecretsBundle
// declares under another name: the legacy CCT install named them with
// underscores, which the secrets operator rejects.
type PasswordMove struct {
	// From and To are the legacy and the chart's entry names.
	From, To string
	// Length is the length the chart's SecretsBundle declares for To. The
	// operator regenerates a value of any other length, so a legacy value
	// that doesn't have it can't be moved as is.
	Length int
}

// VaultLister is what InspectPasswords reads Vault with; *vault.Client is
// one.
type VaultLister interface {
	Mount(ctx context.Context, path string) (vault.MountInfo, error)
	List(ctx context.Context, path string) ([]string, error)
}

// vaultStore is what moving passwords needs from Vault; *vault.Client is it.
type vaultStore interface {
	VaultLister
	Read(ctx context.Context, path string) (vault.Data, error)
	Write(ctx context.Context, path string, d vault.Data) error
}

// PasswordsState is what Vault holds of a step's moves for one app: which
// entries its passwords folder and the folder's legacy/ backups hold, by
// name. InspectPasswords reads it; doctor reports it, Plan decides from it.
type PasswordsState struct {
	// Path is the app's passwords folder.
	Path string
	// Mount is the engine the folder lives in (kv v1).
	Mount vault.MountInfo
	// Entries and Backups are the entry names in Path and Path/legacy.
	Entries, Backups []string
}

// Summary says, for doctor, what the step would do with moves.
func (s PasswordsState) Summary(moves []PasswordMove) string {
	var toMove, moved, lost []string
	for _, m := range moves {
		switch {
		case slices.Contains(s.Entries, m.From):
			toMove = append(toMove, m.From)
		case slices.Contains(s.Backups, m.From):
			moved = append(moved, m.From)
		default:
			lost = append(lost, m.From)
		}
	}
	var parts []string
	if len(toMove) > 0 {
		parts = append(parts, fmt.Sprintf("%d legacy password(s) to move", len(toMove)))
	}
	if len(moved) > 0 {
		parts = append(parts, fmt.Sprintf("%d already moved (backup under %s/)", len(moved), legacyBackupDir))
	}
	if len(lost) > 0 {
		parts = append(parts, fmt.Sprintf("%d not in Vault (%s)", len(lost), strings.Join(lost, ", ")))
	}
	return strings.Join(parts, ", ")
}

// passwordsPath is the Vault folder of an app's passwords: the secrets
// operator's legacy path, <mount>/passwords/<name>.<namespace>, which is the
// same one CCT used for the app (its application id).
func passwordsPath(opts Options) (string, error) {
	if opts.LiveName == "" || opts.LiveNamespace == "" {
		return "", errors.New("no live object to find the app's Vault passwords by")
	}
	return vault.UserlandMount + "passwords/" + opts.LiveName + "." + opts.LiveNamespace, nil
}

// InspectPasswords reads, without changing anything, what Vault holds of
// the app's passwords — exactly the reads Plan starts with, so a doctor run
// that succeeds proves the step can read them too: the mount (it must be
// kv v1, whose plain read/write paths the step uses), then the names in the
// app's passwords folder and in its legacy/ subfolder. No value is read.
func InspectPasswords(ctx context.Context, v VaultLister, opts Options) (PasswordsState, error) {
	path, err := passwordsPath(opts)
	if err != nil {
		return PasswordsState{}, err
	}
	state := PasswordsState{Path: path}
	if state.Mount, err = v.Mount(ctx, vault.UserlandMount); err != nil {
		return PasswordsState{}, err
	}
	if state.Mount.Type != "kv" || state.Mount.Version != "1" {
		return PasswordsState{}, fmt.Errorf("mount %s is %s v%s, not kv v1: the passwords can't be moved by plain read and write", vault.UserlandMount, state.Mount.Type, state.Mount.Version)
	}
	if state.Entries, err = listOptional(ctx, v, path); err != nil {
		return PasswordsState{}, err
	}
	if state.Backups, err = listOptional(ctx, v, path+"/"+legacyBackupDir); err != nil {
		return PasswordsState{}, err
	}
	return state, nil
}

// releaseCutOver reports whether the chart has already replaced the legacy
// app: the legacy Deployment is gone and the release's SecretsBundle (named
// after the release, the legacy live name) exists. Vault then holds nothing
// left to move — the bundle removed the legacy entries — so a re-run plans
// without reaching Vault at all.
func releaseCutOver(ctx context.Context, opts Options, legacyDeployments []Operation) (bool, error) {
	if len(legacyDeployments) > 0 {
		return false, nil
	}
	_, err := kubeclient.GetUnstructured(ctx, opts.Client, gvkSecretsBundle, opts.LiveNamespace, opts.LiveName)
	switch {
	case kubeclient.IsNotFound(err), kubeclient.IsNoMatch(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("fetching SecretsBundle %s/%s: %w", opts.LiveNamespace, opts.LiveName, err)
	}
	return true, nil
}

// planPasswordMoves plans, for each move, a verbatim backup of the legacy
// entry under legacyBackupDir and a copy of its value to the chart's name,
// so the chart's SecretsBundle finds the legacy value already there and
// keeps it. It never plans a write to, or a delete of, a legacy entry.
//
// It is idempotent: what already holds is not planned again (a backup that
// matches, a chart entry that already holds the legacy value), and an
// interrupted run is completed — a missing chart entry is copied from the
// backup once the legacy original is gone. Everything is decided here,
// read-only, before anything is written: a legacy value of the wrong
// length, or a chart entry that already holds another value (the release
// runs with its own), is an error, since moving it would rotate a live
// secret; and each write checks its target again when it runs (see
// guardedWrite). When the legacy entries are gone with no backup — the
// chart was applied before this step ran — nothing can be moved and it
// only warns.
func planPasswordMoves(ctx context.Context, v vaultStore, opts Options, moves []PasswordMove, logger *log.Logger) ([]Operation, error) {
	state, err := InspectPasswords(ctx, v, opts)
	if err != nil {
		return nil, err
	}
	base := state.Path
	read := func(names []string, dir, name string) (vault.Data, error) {
		if !slices.Contains(names, name) {
			return nil, nil
		}
		return readOptional(ctx, v, dir+"/"+name)
	}

	var backups, copies []Operation
	var lost, drifted []string
	for _, m := range moves {
		from, to, backup := base+"/"+m.From, base+"/"+m.To, base+"/"+legacyBackupDir+"/"+m.From
		legacy, err := read(state.Entries, base, m.From)
		if err != nil {
			return nil, err
		}
		current, err := read(state.Entries, base, m.To)
		if err != nil {
			return nil, err
		}
		saved, err := read(state.Backups, base+"/"+legacyBackupDir, m.From)
		if err != nil {
			return nil, err
		}

		// The value to move: the legacy original, or, once the bundle has
		// removed it, the backup a previous run took of it.
		source, sourcePath := legacy, from
		if legacy == nil {
			source, sourcePath = saved, backup
		}
		if source == nil {
			lost = append(lost, m.From)
			continue
		}
		value, ok := source[passwordField]
		if !ok {
			return nil, fmt.Errorf("%s has no %q field to move to %s", sourcePath, passwordField, m.To)
		}
		if len(value) != m.Length {
			return nil, fmt.Errorf("%s holds a %d-character value, but the chart declares %s with %d: the secrets operator would replace it with a new one, so it can't be moved as is",
				sourcePath, len(value), m.To, m.Length)
		}
		if current != nil && current[passwordField] != value {
			if legacy == nil {
				// Moved before, then replaced: nothing to undo here.
				drifted = append(drifted, m.To)
				continue
			}
			return nil, fmt.Errorf("%s already holds another value than %s: the release has its own, and moving the legacy one would rotate a live secret. Decide by hand (see the litellm Notes in the catalog)", to, from)
		}
		if legacy != nil && saved != nil && !maps.Equal(saved, legacy) {
			return nil, fmt.Errorf("%s doesn't match its backup %s: the legacy entry changed after it was backed up; check both by hand", from, backup)
		}

		if legacy != nil && saved == nil {
			backups = append(backups, guardedWrite("back up", base, legacyBackupDir+"/"+m.From, backup, maps.Clone(legacy), v,
				func(cur vault.Data) bool { return maps.Equal(cur, legacy) }))
		}
		// Exactly the one field: an entry with any other (a "user") is
		// regenerated by the operator, the chart declaring none.
		if current == nil || len(current) != 1 {
			copies = append(copies, guardedWrite("copy", base, fmt.Sprintf("%s (from %s)", m.To, sourcePath[len(base)+1:]), to, vault.Data{passwordField: value}, v,
				func(cur vault.Data) bool { return cur[passwordField] == value }))
		}
	}

	if len(lost) > 0 {
		logger.Warningf("the legacy Vault password(s) %s are not in %s: if the release already replaced them, data and keys made with them no longer work (see the litellm Notes in the catalog)",
			strings.Join(lost, ", "), base)
	}
	if len(drifted) > 0 {
		logger.Warningf("%s in %s no longer hold(s) the legacy value backed up under %s/", strings.Join(drifted, ", "), base, legacyBackupDir)
	}
	// Backups first, so an interrupted run never leaves a copy without one.
	return append(backups, copies...), nil
}

// readOptional reads path, nil when it holds nothing.
func readOptional(ctx context.Context, v vaultStore, path string) (vault.Data, error) {
	d, err := v.Read(ctx, path)
	if errors.Is(err, vault.ErrNotFound) {
		return nil, nil
	}
	return d, err
}

// listOptional lists path, empty when it holds nothing.
func listOptional(ctx context.Context, v VaultLister, path string) ([]string, error) {
	names, err := v.List(ctx, path)
	if errors.Is(err, vault.ErrNotFound) {
		return nil, nil
	}
	return names, err
}

// guardedWrite writes d to path when it runs, unless path has meanwhile
// come to hold something unchanged doesn't accept: the plan was decided
// from what Vault held then, and between the plan and its run (the
// confirmation prompt, a dry run before the real one) the chart may have
// been applied and the secrets operator written its own value — which
// this write must never overwrite. kv v1 has no check-and-set, so this
// re-read narrows that window to the write itself rather than closing it.
// An entry already holding d is left as it is.
//
// Its Object only names the entry for the operation's description (folder
// base, name name); the cluster client Apply is given goes unused.
func guardedWrite(action, base, name, path string, d vault.Data, v vaultStore, unchanged func(vault.Data) bool) Operation {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetAPIVersion("vault")
	obj.SetKind(KindVaultPassword)
	obj.SetNamespace(base)
	obj.SetName(name)
	return Operation{Action: action, Object: obj, apply: func(ctx context.Context, _ client.Client) error {
		cur, err := readOptional(ctx, v, path)
		switch {
		case err != nil:
			return err
		case maps.Equal(cur, d):
			return nil
		case cur != nil && !unchanged(cur):
			return fmt.Errorf("%s changed since the plan (the chart may have been applied meanwhile): not overwriting it; run the migration again to re-plan", path)
		}
		return v.Write(ctx, path, d)
	}}
}
