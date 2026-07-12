package releaseactivation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	stateRelativePath = "Library/Application Support/personal-mcp-gateway/release/obsidian"
	lockFileName      = "lock"
	activeDirName     = "active"
	manifestFileName  = "manifest.json"
	candidateFileName = "candidate"
	authorityFileName = "authority"
	previousFileName  = "previous"
	maxArtifactSize   = 256 << 20
)

var (
	// ErrBusy means another process currently owns the release lifecycle lock.
	ErrBusy = errors.New("release activation store is busy")
	// ErrStateConflict means the on-disk slot is not clear for the requested operation.
	ErrStateConflict = errors.New("release activation state conflict")
	// ErrStateMalformed means lifecycle state or an immutable artifact failed validation.
	ErrStateMalformed = errors.New("release activation state malformed")
	// ErrArtifactMismatch means copied bytes no longer match the identity selected by Manager.
	ErrArtifactMismatch = errors.New("release activation artifact mismatch")
)

// ArtifactSources names the exact immutable bytes copied into a transaction.
// Previous must be empty exactly when Manifest.PreviousPresent is false.
type ArtifactSources struct {
	Candidate string
	Authority string
	Previous  string
}

// StoreHookPoint identifies a persistence boundary. Hooks are instance-scoped
// and nil by default; tests use them to inject failures without process-global
// flags that could affect another Store.
type StoreHookPoint string

const (
	StoreBeforeManifestTempWrite StoreHookPoint = "before_manifest_temp_write"
	StoreAfterManifestTempWrite  StoreHookPoint = "after_manifest_temp_write"
	StoreBeforeManifestTempSync  StoreHookPoint = "before_manifest_temp_sync"
	StoreAfterManifestTempSync   StoreHookPoint = "after_manifest_temp_sync"
	StoreBeforeManifestPublish   StoreHookPoint = "before_manifest_publish"
	StoreAfterManifestPublish    StoreHookPoint = "after_manifest_publish"
	StoreBeforeManifestDirSync   StoreHookPoint = "before_manifest_dir_sync"
	StoreAfterManifestDirSync    StoreHookPoint = "after_manifest_dir_sync"
	StoreBeforeActivePublish     StoreHookPoint = "before_active_publish"
	StoreAfterActivePublish      StoreHookPoint = "after_active_publish"
	StoreBeforeActiveParentSync  StoreHookPoint = "before_active_parent_sync"
	StoreAfterActiveParentSync   StoreHookPoint = "after_active_parent_sync"
	StoreBeforeClearPublish      StoreHookPoint = "before_clear_publish"
	StoreAfterClearPublish       StoreHookPoint = "after_clear_publish"
	StoreBeforeClearParentSync   StoreHookPoint = "before_clear_parent_sync"
	StoreAfterClearParentSync    StoreHookPoint = "after_clear_parent_sync"
	StoreBeforeClearCleanup      StoreHookPoint = "before_clear_cleanup"
	StoreAfterClearCleanup       StoreHookPoint = "after_clear_cleanup"
	StoreBeforeOrphanCleanup     StoreHookPoint = "before_orphan_cleanup"
	StoreAfterOrphanCleanup      StoreHookPoint = "after_orphan_cleanup"
)

// StoreHook may return an error to stop the operation at a named boundary.
type StoreHook func(StoreHookPoint) error

// Store owns the fixed per-user release transaction slot.
type Store struct {
	root         string
	effectiveUID int
	hook         StoreHook
}

// LockedStore is the only mutation-capable view of a Store. Close releases the
// process-scoped advisory lock, including on every returned error path.
type LockedStore struct {
	store *Store
	file  *os.File
}

// NewStore locates state from the effective UID's passwd entry. It deliberately
// does not inspect HOME or application configuration.
func NewStore() (*Store, error) {
	uid := os.Geteuid()
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, fmt.Errorf("lookup effective uid %d: %w", uid, err)
	}
	if !filepath.IsAbs(account.HomeDir) {
		return nil, fmt.Errorf("passwd home for effective uid %d is not absolute", uid)
	}
	return NewStoreAt(filepath.Join(account.HomeDir, filepath.FromSlash(stateRelativePath)), uid)
}

// NewStoreAt injects the state root and effective UID for isolated tests. Host
// integrations should use NewStore so environment variables cannot move state.
func NewStoreAt(root string, effectiveUID int) (*Store, error) {
	return NewStoreAtWithHook(root, effectiveUID, nil)
}

// NewStoreAtWithHook is NewStoreAt with an instance-local persistence hook.
// It exists for deterministic fault-injection tests.
func NewStoreAtWithHook(root string, effectiveUID int, hook StoreHook) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("release state root must be absolute")
	}
	return &Store{root: filepath.Clean(root), effectiveUID: effectiveUID, hook: hook}, nil
}

func (s *Store) Root() string { return s.root }

// ActiveAuthorityPath, ActiveCandidatePath, and ActivePreviousPath expose only
// the three fixed artifact locations. Callers must validate the corresponding
// manifest through Inspect or LockedStore.Load before executing or installing
// bytes from these paths.
func (s *Store) ActiveAuthorityPath() string {
	return filepath.Join(s.root, activeDirName, authorityFileName)
}

func (s *Store) ActiveCandidatePath() string {
	return filepath.Join(s.root, activeDirName, candidateFileName)
}

func (s *Store) ActivePreviousPath() string {
	return filepath.Join(s.root, activeDirName, previousFileName)
}

// HashRegular hashes a bounded, no-follow regular file. Runtime reconciliation
// uses it for installed targets and pinned host inputs whose modes are not owned
// by the transaction store.
func HashRegular(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	linked, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
		return "", errors.New("path is not the opened regular inode")
	}
	if info.Size() > maxArtifactSize {
		return "", errors.New("file exceeds size limit")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxArtifactSize+1))
	if err != nil {
		return "", err
	}
	if n > maxArtifactSize {
		return "", errors.New("file exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NewReleaseID returns a full, unguessable 256-bit lowercase identifier.
func NewReleaseID() (ReleaseID, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate release id: %w", err)
	}
	return ReleaseID(hex.EncodeToString(raw)), nil
}

// Acquire takes the permanent, fail-fast lifecycle lock. The inode lives
// outside active/ and is never renamed or removed by Store operations.
func (s *Store) Acquire() (*LockedStore, error) {
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, lockFileName)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lifecycle lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
		}
	}()
	if err := validateOpenRegular(f, path, 0o600); err != nil {
		return nil, fmt.Errorf("validate lifecycle lock: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock lifecycle state: %w", err)
	}
	// Re-read the path after acquiring: replacing the pathname must not trick a
	// contender into believing it shares our permanent lock inode.
	if err := validateOpenRegular(f, path, 0o600); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, fmt.Errorf("revalidate lifecycle lock: %w", err)
	}
	keep = true
	return &LockedStore{store: s, file: f}, nil
}

func (l *LockedStore) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	fd := int(l.file.Fd())
	unlockErr := unix.Flock(fd, unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}

// Inspect validates a complete transaction without acquiring the lock. It is
// intended only for optimistic dispatcher selection; effectful callers must
// acquire and re-read with LockedStore.Load.
func (s *Store) Inspect() (*Manifest, error) {
	if err := s.validateRootIfPresent(); err != nil {
		return nil, err
	}
	return s.loadActive()
}

// Load returns nil,nil for clear and never mutates the store. Callers may run
// CleanupOrphans only after authenticating the loaded transaction.
func (l *LockedStore) Load() (*Manifest, error) {
	if err := l.valid(); err != nil {
		return nil, err
	}
	return l.store.loadActive()
}

// CleanupOrphans removes only non-authoritative transaction remnants. The
// caller must first Load and authenticate active authority under this lock.
func (l *LockedStore) CleanupOrphans() error {
	if err := l.valid(); err != nil {
		return err
	}
	if err := l.store.fire(StoreBeforeOrphanCleanup); err != nil {
		return err
	}
	active := filepath.Join(l.store.root, activeDirName)
	if err := cleanupManifestTemps(active); err != nil {
		return err
	}
	if err := l.cleanupRootOrphans(); err != nil {
		return err
	}
	return l.store.fire(StoreAfterOrphanCleanup)
}

// Prepare copies and hashes immutable artifacts, writes a synced prepared
// manifest in active.next.<id>, then atomically publishes active/.
func (l *LockedStore) Prepare(manifest Manifest, sources ArtifactSources) (*Manifest, error) {
	if err := l.valid(); err != nil {
		return nil, err
	}
	if err := l.store.ensureRoot(); err != nil {
		return nil, err
	}
	active := filepath.Join(l.store.root, activeDirName)
	if _, err := os.Lstat(active); err == nil {
		return nil, ErrStateConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect active transaction: %w", err)
	}
	if err := ValidatePrepareTopology(l.store, sources, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStateMalformed, err)
	}

	if !validReleaseID(manifest.ID) {
		return nil, fmt.Errorf("%w: invalid release id", ErrStateMalformed)
	}
	if manifest.State != StatePrepared {
		return nil, fmt.Errorf("%w: prepare requires prepared state", ErrStateMalformed)
	}
	if manifest.PreviousPresent != (sources.Previous != "") {
		return nil, fmt.Errorf("%w: previous artifact presence mismatch", ErrStateMalformed)
	}

	next := filepath.Join(l.store.root, "active.next."+string(manifest.ID))
	if err := os.Mkdir(next, 0o700); err != nil {
		return nil, fmt.Errorf("create next transaction: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(next)
		}
	}()

	candidateHash, err := copyRegular(sources.Candidate, filepath.Join(next, candidateFileName), 0o500)
	if err != nil {
		return nil, fmt.Errorf("copy candidate artifact: %w", err)
	}
	authorityHash, err := copyRegular(sources.Authority, filepath.Join(next, authorityFileName), 0o500)
	if err != nil {
		return nil, fmt.Errorf("copy authority artifact: %w", err)
	}
	if candidateHash != manifest.CandidateSHA256 || authorityHash != manifest.AuthoritySHA256 {
		return nil, fmt.Errorf("%w: selected artifact changed before publication", ErrArtifactMismatch)
	}
	if manifest.PreviousPresent {
		previousHash, copyErr := copyRegular(sources.Previous, filepath.Join(next, previousFileName), 0o500)
		if copyErr != nil {
			return nil, fmt.Errorf("copy previous artifact: %w", copyErr)
		}
		if previousHash != manifest.PreviousSHA256 {
			return nil, fmt.Errorf("%w: selected previous artifact changed before publication", ErrArtifactMismatch)
		}
	}
	manifest.Version = ManifestVersion
	manifest.EffectiveUID = l.store.effectiveUID
	manifest.CandidateFile = candidateFileName
	manifest.AuthorityFile = authorityFileName
	if manifest.PreviousPresent {
		manifest.PreviousFile = previousFileName
	} else {
		manifest.PreviousFile = ""
		manifest.PreviousSHA256 = ""
	}
	if err := ValidateActiveTopology(l.store, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStateMalformed, err)
	}
	if err := validateManifest(&manifest, l.store.effectiveUID); err != nil {
		return nil, err
	}
	if err := l.store.writeManifestAtomic(next, &manifest); err != nil {
		return nil, err
	}
	if err := l.store.fire(StoreBeforeActivePublish); err != nil {
		return nil, err
	}
	if err := os.Rename(next, active); err != nil {
		return nil, fmt.Errorf("publish active transaction: %w", err)
	}
	published = true
	if err := l.store.fire(StoreAfterActivePublish); err != nil {
		return nil, err
	}
	if err := l.store.fire(StoreBeforeActiveParentSync); err != nil {
		return nil, err
	}
	if err := syncDir(l.store.root); err != nil {
		return nil, fmt.Errorf("sync published transaction: %w", err)
	}
	if err := l.store.fire(StoreAfterActiveParentSync); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Rewrite atomically replaces manifest.json inside active/. Immutable artifact
// identity is revalidated both before and after the state change.
func (l *LockedStore) Rewrite(manifest Manifest) error {
	if err := l.valid(); err != nil {
		return err
	}
	current, err := l.store.loadActive()
	if err != nil {
		return err
	}
	if current == nil {
		return ErrStateConflict
	}
	// State is the only mutable manifest field. Target and supervision bindings
	// are recovery authority just as much as the copied artifact hashes are.
	withCurrentState := manifest
	withCurrentState.State = current.State
	if withCurrentState != *current {
		return fmt.Errorf("%w: immutable transaction identity changed", ErrStateConflict)
	}
	if err := validateManifest(&manifest, l.store.effectiveUID); err != nil {
		return err
	}
	return l.store.writeManifestAtomic(filepath.Join(l.store.root, activeDirName), &manifest)
}

// Clear atomically removes active authority by renaming it to cleanup.<id>.
// The rename is the process-crash clear point. Returned warnings concern only
// best-effort parent sync/removal after the slot is already clear.
func (l *LockedStore) Clear(id ReleaseID) ([]error, error) {
	if err := l.valid(); err != nil {
		return nil, err
	}
	current, err := l.store.loadActive()
	if err != nil {
		return nil, err
	}
	if current == nil || current.ID != id {
		return nil, ErrStateConflict
	}
	active := filepath.Join(l.store.root, activeDirName)
	cleanup := filepath.Join(l.store.root, "cleanup."+string(id))
	if _, err := os.Lstat(cleanup); err == nil {
		if removeErr := os.RemoveAll(cleanup); removeErr != nil {
			return nil, fmt.Errorf("remove prior cleanup orphan: %w", removeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect cleanup destination: %w", err)
	}
	if err := l.store.fire(StoreBeforeClearPublish); err != nil {
		return nil, err
	}
	if err := os.Rename(active, cleanup); err != nil {
		return nil, fmt.Errorf("commit clear transaction: %w", err)
	}
	if err := l.store.fire(StoreAfterClearPublish); err != nil {
		return nil, err
	}
	var warnings []error
	if err := l.store.fire(StoreBeforeClearParentSync); err != nil {
		warnings = append(warnings, err)
	} else if err := syncDir(l.store.root); err != nil {
		warnings = append(warnings, fmt.Errorf("sync cleared transaction: %w", err))
	} else if err := l.store.fire(StoreAfterClearParentSync); err != nil {
		warnings = append(warnings, err)
	}
	if err := l.store.fire(StoreBeforeClearCleanup); err != nil {
		warnings = append(warnings, err)
	} else if err := os.RemoveAll(cleanup); err != nil {
		warnings = append(warnings, fmt.Errorf("remove cleanup transaction: %w", err))
	} else if err := l.store.fire(StoreAfterClearCleanup); err != nil {
		warnings = append(warnings, err)
	}
	return warnings, nil
}

func (l *LockedStore) valid() error {
	if l == nil || l.store == nil || l.file == nil {
		return errors.New("release activation lock is not held")
	}
	if err := validateOpenRegular(l.file, filepath.Join(l.store.root, lockFileName), 0o600); err != nil {
		return fmt.Errorf("lifecycle lock inode changed: %w", err)
	}
	return nil
}

func (s *Store) ensureRoot() error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create release state root: %w", err)
	}
	return s.validateRootIfPresent()
}

func (s *Store) validateRootIfPresent() error {
	info, err := os.Lstat(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect release state root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: release state root is not a directory", ErrStateMalformed)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: release state root permissions are not 0700", ErrStateMalformed)
	}
	return nil
}

func (s *Store) fire(point StoreHookPoint) error {
	if s.hook == nil {
		return nil
	}
	if err := s.hook(point); err != nil {
		return fmt.Errorf("store hook %s: %w", point, err)
	}
	return nil
}

func (s *Store) loadActive() (*Manifest, error) {
	active := filepath.Join(s.root, activeDirName)
	info, err := os.Lstat(active)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect active transaction: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("%w: invalid active transaction directory", ErrStateMalformed)
	}
	manifestPath := filepath.Join(active, manifestFileName)
	data, err := readRegular(manifestPath, 0o600, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("%w: read manifest: %v", ErrStateMalformed, err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("%w: decode manifest: %v", ErrStateMalformed, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: decode manifest: %v", ErrStateMalformed, err)
	}
	if err := validateManifest(&manifest, s.effectiveUID); err != nil {
		return nil, err
	}
	if err := validateArtifact(active, manifest.CandidateFile, candidateFileName, manifest.CandidateSHA256); err != nil {
		return nil, err
	}
	if err := validateArtifact(active, manifest.AuthorityFile, authorityFileName, manifest.AuthoritySHA256); err != nil {
		return nil, err
	}
	if manifest.PreviousPresent {
		if err := validateArtifact(active, manifest.PreviousFile, previousFileName, manifest.PreviousSHA256); err != nil {
			return nil, err
		}
	} else if manifest.PreviousFile != "" || manifest.PreviousSHA256 != "" {
		return nil, fmt.Errorf("%w: unexpected previous artifact", ErrStateMalformed)
	}
	if err := ValidateActiveTopology(s, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStateMalformed, err)
	}
	return &manifest, nil
}

func validateManifest(m *Manifest, effectiveUID int) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("%w: unsupported manifest version", ErrStateMalformed)
	}
	if m.State == StateClear || !validReleaseID(m.ID) {
		return fmt.Errorf("%w: invalid transaction identity", ErrStateMalformed)
	}
	if m.EffectiveUID != effectiveUID {
		return fmt.Errorf("%w: effective uid mismatch", ErrStateMalformed)
	}
	if !ValidLaunchAgentLabel(m.LaunchAgentLabel) {
		return fmt.Errorf("%w: invalid launch agent label", ErrStateMalformed)
	}
	if !validSHA256(m.CandidateSHA256) || !validSHA256(m.AuthoritySHA256) {
		return fmt.Errorf("%w: invalid artifact hash", ErrStateMalformed)
	}
	if m.CandidateFile != candidateFileName || m.AuthorityFile != authorityFileName {
		return fmt.Errorf("%w: invalid artifact name", ErrStateMalformed)
	}
	if m.PreviousPresent {
		if m.PreviousFile != previousFileName || !validSHA256(m.PreviousSHA256) {
			return fmt.Errorf("%w: invalid previous artifact", ErrStateMalformed)
		}
	} else if m.PreviousFile != "" || m.PreviousSHA256 != "" {
		return fmt.Errorf("%w: unexpected previous artifact", ErrStateMalformed)
	}
	if validationErr := ValidateSnapshot(Snapshot{Manifest: m}); validationErr != nil {
		return fmt.Errorf("%w: %s", ErrStateMalformed, validationErr.Message)
	}
	return nil
}

func validateArtifact(active, recorded, expected, wantHash string) error {
	if recorded != expected {
		return fmt.Errorf("%w: invalid artifact name", ErrStateMalformed)
	}
	path := filepath.Join(active, expected)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: validate %s artifact: %v", ErrStateMalformed, expected, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	if err := validateOpenRegular(file, path, 0o500); err != nil {
		return fmt.Errorf("%w: validate %s artifact: %v", ErrStateMalformed, expected, err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() > maxArtifactSize {
		return fmt.Errorf("%w: validate %s artifact size", ErrStateMalformed, expected)
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maxArtifactSize+1))
	if err != nil || read > maxArtifactSize {
		return fmt.Errorf("%w: validate %s artifact content", ErrStateMalformed, expected)
	}
	if hex.EncodeToString(hash.Sum(nil)) != wantHash {
		return fmt.Errorf("%w: %s artifact hash mismatch", ErrStateMalformed, expected)
	}
	return nil
}

func (s *Store) writeManifestAtomic(dir string, manifest *Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	suffix, err := randomHex(12)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".manifest."+suffix+".tmp")
	fd, err := unix.Open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create manifest temp: %w", err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tmp)
		}
	}()
	if err := s.fire(StoreBeforeManifestTempWrite); err != nil {
		return err
	}
	if err := writeAll(f, data); err != nil {
		return fmt.Errorf("write manifest temp: %w", err)
	}
	if err := s.fire(StoreAfterManifestTempWrite); err != nil {
		return err
	}
	if err := s.fire(StoreBeforeManifestTempSync); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync manifest temp: %w", err)
	}
	if err := s.fire(StoreAfterManifestTempSync); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close manifest temp: %w", err)
	}
	if err := s.fire(StoreBeforeManifestPublish); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, manifestFileName)); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	keep = true
	if err := s.fire(StoreAfterManifestPublish); err != nil {
		return err
	}
	if err := s.fire(StoreBeforeManifestDirSync); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync manifest directory: %w", err)
	}
	return s.fire(StoreAfterManifestDirSync)
}

func copyRegular(source, destination string, mode os.FileMode) (string, error) {
	inFD, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	in := os.NewFile(uintptr(inFD), source)
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("source is not a regular file")
	}
	if info.Size() > maxArtifactSize {
		return "", errors.New("source exceeds artifact size limit")
	}
	outFD, err := unix.Open(destination, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return "", err
	}
	out := os.NewFile(uintptr(outFD), destination)
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, maxArtifactSize+1))
	if err != nil {
		return "", err
	}
	if n > maxArtifactSize {
		return "", errors.New("source exceeds artifact size limit")
	}
	if err := out.Chmod(mode); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	ok = true
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readRegular(path string, mode os.FileMode, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := validateOpenRegular(f, path, mode); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

func validateOpenRegular(f *os.File, path string, mode os.FileMode) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	linked, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return errors.New("path is not the opened regular inode")
	}
	if opened.Mode().Perm() != mode.Perm() {
		return fmt.Errorf("permissions are %04o, want %04o", opened.Mode().Perm(), mode.Perm())
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func randomHex(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func cleanupManifestTemps(active string) error {
	entries, err := os.ReadDir(active)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".manifest.") && strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(filepath.Join(active, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (l *LockedStore) cleanupRootOrphans() error {
	entries, err := os.ReadDir(l.store.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "active.next.") || strings.HasPrefix(name, "cleanup.") {
			if err := os.RemoveAll(filepath.Join(l.store.root, name)); err != nil {
				return err
			}
		}
	}
	return nil
}
