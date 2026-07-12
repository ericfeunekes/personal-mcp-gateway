package releaseactivation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathTopologyKind distinguishes untrusted source selection from malformed
// active-store layout without including private paths in the error surface.
type PathTopologyKind string

const (
	PathTopologySourceNotAbsolute  PathTopologyKind = "source_not_absolute"
	PathTopologySourceInsideStore  PathTopologyKind = "source_inside_store"
	PathTopologySourceAlias        PathTopologyKind = "source_alias"
	PathTopologyActiveArtifact     PathTopologyKind = "active_artifact"
	PathTopologyRuntimeNotAbsolute PathTopologyKind = "runtime_not_absolute"
	PathTopologyRuntimeInsideStore PathTopologyKind = "runtime_inside_store"
	PathTopologyRuntimeAlias       PathTopologyKind = "runtime_alias"
	PathTopologyPreviousTarget     PathTopologyKind = "previous_target_mismatch"
)

// PathTopologyError identifies the conflicting logical roles. It deliberately
// omits path values so callers can safely sanitize it at a public boundary.
type PathTopologyError struct {
	Kind      PathTopologyKind
	Role      string
	OtherRole string
}

func (e *PathTopologyError) Error() string {
	if e.OtherRole != "" {
		return fmt.Sprintf("path topology %s: %s conflicts with %s", e.Kind, e.Role, e.OtherRole)
	}
	return fmt.Sprintf("path topology %s: %s", e.Kind, e.Role)
}

// ValidLaunchAgentLabel accepts a bounded launchd label that is also safe as a
// single plist filename component. Empty segments, path separators, traversal
// components, whitespace, and non-ASCII punctuation are rejected.
func ValidLaunchAgentLabel(label string) bool {
	if len(label) == 0 || len(label) > 255 || label == "." || label == ".." || filepath.Base(label) != label {
		return false
	}
	for _, segment := range strings.Split(label, ".") {
		if segment == "" || !asciiAlphaNumeric(segment[0]) || !asciiAlphaNumeric(segment[len(segment)-1]) {
			return false
		}
		for i := 1; i < len(segment)-1; i++ {
			if !asciiAlphaNumeric(segment[i]) && segment[i] != '-' && segment[i] != '_' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// ValidateSourceTopology proves that immutable inputs are external to Store
// and do not alias one another lexically, through symlinked ancestors, or by
// inode. Previous may be empty for a first install.
func ValidateSourceTopology(store *Store, sources ArtifactSources) error {
	if store == nil {
		return &PathTopologyError{Kind: PathTopologySourceInsideStore, Role: "store"}
	}
	type selectedPath struct {
		role      string
		path      string
		canonical string
		info      os.FileInfo
	}
	selected := []selectedPath{{role: "candidate", path: sources.Candidate}, {role: "authority", path: sources.Authority}}
	if sources.Previous != "" {
		selected = append(selected, selectedPath{role: "previous", path: sources.Previous})
	}
	canonicalRoot, err := canonicalizeAllowMissing(store.root)
	if err != nil {
		return fmt.Errorf("canonicalize store root: %w", err)
	}
	for i := range selected {
		if !filepath.IsAbs(selected[i].path) {
			return &PathTopologyError{Kind: PathTopologySourceNotAbsolute, Role: selected[i].role}
		}
		selected[i].canonical, err = canonicalizeAllowMissing(selected[i].path)
		if err != nil {
			return fmt.Errorf("canonicalize %s source: %w", selected[i].role, err)
		}
		if pathWithin(canonicalRoot, selected[i].canonical) {
			return &PathTopologyError{Kind: PathTopologySourceInsideStore, Role: selected[i].role}
		}
		selected[i].info, err = os.Stat(selected[i].path)
		if err != nil {
			return fmt.Errorf("stat %s source: %w", selected[i].role, err)
		}
		for j := 0; j < i; j++ {
			if selected[i].canonical == selected[j].canonical || os.SameFile(selected[i].info, selected[j].info) {
				return &PathTopologyError{Kind: PathTopologySourceAlias, Role: selected[i].role, OtherRole: selected[j].role}
			}
		}
	}
	return nil
}

// ValidatePrepareTopology validates the complete pre-publication role graph.
// Previous and target are one logical role and therefore must identify the
// same path; candidate and authority must be distinct from every operational
// role before Manager observes or Store copies them.
func ValidatePrepareTopology(store *Store, sources ArtifactSources, manifest *Manifest) error {
	if err := ValidateSourceTopology(store, sources); err != nil {
		return err
	}
	if store == nil || manifest == nil {
		return &PathTopologyError{Kind: PathTopologyRuntimeAlias, Role: "manifest"}
	}
	if err := validateRuntimeTopology(store, manifest); err != nil {
		return err
	}
	type rolePath struct {
		role      string
		path      string
		canonical string
		info      os.FileInfo
	}
	resolve := func(role, path string) (rolePath, error) {
		resolved := rolePath{role: role, path: path}
		var err error
		resolved.canonical, err = canonicalizeAllowMissing(path)
		if err != nil {
			return resolved, err
		}
		resolved.info, err = os.Stat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return resolved, err
		}
		return resolved, nil
	}
	selected := []rolePath{}
	for _, source := range []struct{ role, path string }{{"candidate", sources.Candidate}, {"authority", sources.Authority}} {
		resolved, err := resolve(source.role, source.path)
		if err != nil {
			return fmt.Errorf("resolve %s source: %w", source.role, err)
		}
		selected = append(selected, resolved)
	}
	if sources.Previous != "" {
		previous, err := resolve("previous", sources.Previous)
		if err != nil {
			return fmt.Errorf("resolve previous source: %w", err)
		}
		target, err := resolve("target", manifest.TargetPath)
		if err != nil {
			return fmt.Errorf("resolve target runtime path: %w", err)
		}
		if !samePathIdentity(previous.canonical, previous.info, target.canonical, target.info) {
			return &PathTopologyError{Kind: PathTopologyPreviousTarget, Role: "previous", OtherRole: "target"}
		}
	}
	operational := []struct{ role, path string }{
		{"target", manifest.TargetPath},
		{"plist", manifest.PlistPath},
		{"wrapper", manifest.WrapperPath},
		{"mcp_wrapper", manifest.MCPWrapperPath},
		{"environment", manifest.EnvironmentPath},
		{"health_url", manifest.HealthURLFile},
		{"stdout", manifest.StdoutPath},
		{"stderr", manifest.StderrPath},
	}
	for _, operation := range operational {
		resolved, err := resolve(operation.role, operation.path)
		if err != nil {
			return fmt.Errorf("resolve %s runtime path: %w", operation.role, err)
		}
		for _, source := range selected {
			if samePathIdentity(source.canonical, source.info, resolved.canonical, resolved.info) {
				return &PathTopologyError{Kind: PathTopologyRuntimeAlias, Role: operation.role, OtherRole: source.role}
			}
		}
	}
	return nil
}

func samePathIdentity(leftCanonical string, leftInfo os.FileInfo, rightCanonical string, rightInfo os.FileInfo) bool {
	return leftCanonical == rightCanonical || leftInfo != nil && rightInfo != nil && os.SameFile(leftInfo, rightInfo)
}

// ValidateActiveTopology proves that manifest artifact names resolve to the
// exact fixed direct children of this Store's active directory.
func ValidateActiveTopology(store *Store, manifest *Manifest) error {
	if store == nil || manifest == nil {
		return &PathTopologyError{Kind: PathTopologyActiveArtifact, Role: "manifest"}
	}
	want := []struct {
		role string
		name string
		want string
	}{
		{role: "candidate", name: manifest.CandidateFile, want: candidateFileName},
		{role: "authority", name: manifest.AuthorityFile, want: authorityFileName},
	}
	if manifest.PreviousPresent {
		want = append(want, struct {
			role string
			name string
			want string
		}{role: "previous", name: manifest.PreviousFile, want: previousFileName})
	}
	canonicalActive, err := canonicalizeAllowMissing(filepath.Join(store.root, activeDirName))
	if err != nil {
		return fmt.Errorf("canonicalize active store: %w", err)
	}
	for _, artifact := range want {
		if artifact.name != artifact.want || filepath.Base(artifact.name) != artifact.name {
			return &PathTopologyError{Kind: PathTopologyActiveArtifact, Role: artifact.role}
		}
		path := filepath.Join(store.root, activeDirName, artifact.name)
		canonical, err := canonicalizeAllowMissing(path)
		if err != nil {
			return fmt.Errorf("canonicalize active %s: %w", artifact.role, err)
		}
		if canonical != filepath.Join(canonicalActive, artifact.want) {
			return &PathTopologyError{Kind: PathTopologyActiveArtifact, Role: artifact.role}
		}
	}
	return validateRuntimeTopology(store, manifest)
}

func validateRuntimeTopology(store *Store, manifest *Manifest) error {
	type runtimePath struct {
		role      string
		path      string
		canonical string
		info      os.FileInfo
	}
	paths := []runtimePath{
		{role: "target", path: manifest.TargetPath},
		{role: "plist", path: manifest.PlistPath},
		{role: "wrapper", path: manifest.WrapperPath},
		{role: "mcp_wrapper", path: manifest.MCPWrapperPath},
		{role: "environment", path: manifest.EnvironmentPath},
		{role: "health_url", path: manifest.HealthURLFile},
		{role: "stdout", path: manifest.StdoutPath},
		{role: "stderr", path: manifest.StderrPath},
	}
	canonicalRoot, err := canonicalizeAllowMissing(store.root)
	if err != nil {
		return fmt.Errorf("canonicalize store root: %w", err)
	}
	for i := range paths {
		if !filepath.IsAbs(paths[i].path) {
			return &PathTopologyError{Kind: PathTopologyRuntimeNotAbsolute, Role: paths[i].role}
		}
		paths[i].canonical, err = canonicalizeAllowMissing(paths[i].path)
		if err != nil {
			return fmt.Errorf("canonicalize %s runtime path: %w", paths[i].role, err)
		}
		if pathWithin(canonicalRoot, paths[i].canonical) {
			return &PathTopologyError{Kind: PathTopologyRuntimeInsideStore, Role: paths[i].role}
		}
		paths[i].info, err = os.Stat(paths[i].path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s runtime path: %w", paths[i].role, err)
		}
		for j := 0; j < i; j++ {
			sameInode := paths[i].info != nil && paths[j].info != nil && os.SameFile(paths[i].info, paths[j].info)
			if paths[i].canonical == paths[j].canonical || sameInode {
				return &PathTopologyError{Kind: PathTopologyRuntimeAlias, Role: paths[i].role, OtherRole: paths[j].role}
			}
		}
	}
	return nil
}

func canonicalizeAllowMissing(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", errors.New("path is not absolute")
	}
	current := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
