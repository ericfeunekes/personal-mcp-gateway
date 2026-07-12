package releaseactivation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestNewStoreIgnoresHostileHome(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "attacker-controlled"))
	store, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(account.HomeDir, filepath.FromSlash(stateRelativePath))
	if store.Root() != want {
		t.Fatalf("root = %q, want passwd-derived %q", store.Root(), want)
	}
}

func TestReleaseIDAndHashRegularAreStrictAndNoFollow(t *testing.T) {
	id, err := NewReleaseID()
	if err != nil {
		t.Fatal(err)
	}
	if !validReleaseID(id) {
		t.Fatalf("generated release ID %q is not canonical", id)
	}
	dir := t.TempDir()
	path := writeFixtureFile(t, dir, "regular", "hash me")
	hash, err := HashRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256(hash) {
		t.Fatalf("hash %q is not canonical", hash)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := HashRegular(link); err == nil {
		t.Fatal("HashRegular followed a symlink")
	}
}

func TestStorePrepareLoadRewriteAndClear(t *testing.T) {
	store, sources := newStoreFixture(t, true)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()

	manifest := storeTestManifest(true)
	manifest.TargetPath = sources.Previous
	prepared, err := locked.Prepare(manifest, sources)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Version != ManifestVersion || prepared.CandidateSHA256 == "" || prepared.AuthoritySHA256 == "" || prepared.PreviousSHA256 == "" {
		t.Fatalf("prepared manifest missing store-owned facts: %#v", prepared)
	}
	assertMode(t, store.Root(), 0o700)
	assertMode(t, filepath.Join(store.Root(), lockFileName), 0o600)
	active := filepath.Join(store.Root(), activeDirName)
	assertMode(t, active, 0o700)
	assertMode(t, filepath.Join(active, manifestFileName), 0o600)
	assertMode(t, filepath.Join(active, candidateFileName), 0o500)
	assertMode(t, filepath.Join(active, authorityFileName), 0o500)
	assertMode(t, filepath.Join(active, previousFileName), 0o500)

	manifestTemp := filepath.Join(active, ".manifest.interrupted.tmp")
	if err := os.WriteFile(manifestTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	nextOrphan := filepath.Join(store.Root(), "active.next.orphan")
	if err := os.Mkdir(nextOrphan, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupOrphan := filepath.Join(store.Root(), "cleanup.orphan")
	if err := os.Mkdir(cleanupOrphan, 0o700); err != nil {
		t.Fatal(err)
	}
	loaded, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != prepared.ID || loaded.State != StatePrepared {
		t.Fatalf("loaded = %#v, want prepared %s", loaded, prepared.ID)
	}
	if _, err := os.Lstat(manifestTemp); err != nil {
		t.Fatalf("Load mutated manifest temp: %v", err)
	}
	if _, err := os.Lstat(nextOrphan); err != nil {
		t.Fatalf("Load mutated next orphan: %v", err)
	}
	if _, err := os.Lstat(cleanupOrphan); err != nil {
		t.Fatalf("Load mutated cleanup orphan: %v", err)
	}
	if err := locked.CleanupOrphans(); err != nil {
		t.Fatal(err)
	}
	assertNotExist(t, manifestTemp)
	assertNotExist(t, nextOrphan)
	assertNotExist(t, cleanupOrphan)

	loaded.State = StatePending
	if err := locked.Rewrite(*loaded); err != nil {
		t.Fatal(err)
	}
	rewritten, err := locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.State != StatePending {
		t.Fatalf("state = %q, want %q", rewritten.State, StatePending)
	}
	tamperedBinding := *rewritten
	tamperedBinding.TargetPath = "/tmp/different-target"
	if err := locked.Rewrite(tamperedBinding); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Rewrite mutable target error = %v, want ErrStateConflict", err)
	}
	entries, err := os.ReadDir(active)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".manifest.") {
			t.Fatalf("atomic rewrite left temporary file %q", entry.Name())
		}
	}

	lockInfoBefore, err := os.Stat(filepath.Join(store.Root(), lockFileName))
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := locked.Clear(prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("clear warnings: %v", warnings)
	}
	if got, err := locked.Load(); err != nil || got != nil {
		t.Fatalf("load after clear = %#v, %v; want clear", got, err)
	}
	lockInfoAfter, err := os.Stat(filepath.Join(store.Root(), lockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(lockInfoBefore, lockInfoAfter) {
		t.Fatal("clear replaced permanent lock inode")
	}
}

func TestStorePrepareRejectsPreselectedHashMismatchWithoutPublishing(t *testing.T) {
	store, sources := newStoreFixture(t, true)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	manifest := storeTestManifest(true)
	manifest.TargetPath = sources.Previous
	manifest.CandidateSHA256 = strings.Repeat("0", 64)
	if _, err := locked.Prepare(manifest, sources); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("Prepare error = %v, want ErrArtifactMismatch", err)
	}
	if got, err := locked.Load(); err != nil || got != nil {
		t.Fatalf("hash-mismatched prepare published state: %#v, %v", got, err)
	}
	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "active") {
			t.Fatalf("hash-mismatched prepare retained %q", entry.Name())
		}
	}
}

func TestValidateSourceTopologyRejectsCanonicalAndInodeAliases(t *testing.T) {
	store, sources := newStoreFixture(t, false)
	if err := ValidateSourceTopology(store, sources); err != nil {
		t.Fatal(err)
	}

	t.Run("same inode", func(t *testing.T) {
		hardlink := filepath.Join(t.TempDir(), "authority-hardlink")
		if err := os.Link(sources.Candidate, hardlink); err != nil {
			t.Fatal(err)
		}
		err := ValidateSourceTopology(store, ArtifactSources{Candidate: sources.Candidate, Authority: hardlink})
		var topologyErr *PathTopologyError
		if !errors.As(err, &topologyErr) || topologyErr.Kind != PathTopologySourceAlias {
			t.Fatalf("error = %v, want source alias", err)
		}
	})

	t.Run("symlinked ancestor into store", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(store.Root(), "source-dir"), 0o700); err != nil {
			t.Fatal(err)
		}
		inside := writeFixtureFile(t, filepath.Join(store.Root(), "source-dir"), "candidate", "inside")
		aliasParent := filepath.Join(t.TempDir(), "store-alias")
		if err := os.Symlink(store.Root(), aliasParent); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(aliasParent, "source-dir", filepath.Base(inside))
		err := ValidateSourceTopology(store, ArtifactSources{Candidate: alias, Authority: sources.Authority})
		var topologyErr *PathTopologyError
		if !errors.As(err, &topologyErr) || topologyErr.Kind != PathTopologySourceInsideStore {
			t.Fatalf("error = %v, want source inside store", err)
		}
	})
}

func TestValidateActiveTopologyRejectsOperationalRoleAliases(t *testing.T) {
	tests := []struct {
		name string
		kind PathTopologyKind
		set  func(t *testing.T, store *Store, manifest *Manifest)
	}{
		{
			name: "target health alias",
			kind: PathTopologyRuntimeAlias,
			set: func(_ *testing.T, _ *Store, manifest *Manifest) {
				manifest.HealthURLFile = manifest.TargetPath
			},
		},
		{
			name: "runtime path inside store",
			kind: PathTopologyRuntimeInsideStore,
			set: func(_ *testing.T, store *Store, manifest *Manifest) {
				manifest.EnvironmentPath = filepath.Join(store.Root(), "environment")
			},
		},
		{
			name: "existing hardlink",
			kind: PathTopologyRuntimeAlias,
			set: func(t *testing.T, _ *Store, manifest *Manifest) {
				dir := t.TempDir()
				manifest.WrapperPath = writeFixtureFile(t, dir, "wrapper", "same inode")
				manifest.MCPWrapperPath = filepath.Join(dir, "mcp-wrapper")
				if err := os.Link(manifest.WrapperPath, manifest.MCPWrapperPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked parent into store",
			kind: PathTopologyRuntimeInsideStore,
			set: func(t *testing.T, store *Store, manifest *Manifest) {
				inside := filepath.Join(store.Root(), "runtime")
				if err := os.MkdirAll(inside, 0o700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(t.TempDir(), "runtime-alias")
				if err := os.Symlink(inside, alias); err != nil {
					t.Fatal(err)
				}
				manifest.StdoutPath = filepath.Join(alias, "stdout.log")
			},
		},
		{
			name: "absent leaf aliases",
			kind: PathTopologyRuntimeAlias,
			set: func(t *testing.T, _ *Store, manifest *Manifest) {
				realParent := t.TempDir()
				aliasParent := filepath.Join(t.TempDir(), "logs-alias")
				if err := os.Symlink(realParent, aliasParent); err != nil {
					t.Fatal(err)
				}
				manifest.StdoutPath = filepath.Join(realParent, "not-created.log")
				manifest.StderrPath = filepath.Join(aliasParent, "not-created.log")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStoreAt(filepath.Join(t.TempDir(), "state"), os.Geteuid())
			if err != nil {
				t.Fatal(err)
			}
			manifest := storeTestManifest(false)
			tt.set(t, store, &manifest)
			err = ValidateActiveTopology(store, &manifest)
			var topologyErr *PathTopologyError
			if !errors.As(err, &topologyErr) || topologyErr.Kind != tt.kind {
				t.Fatalf("error = %v, want topology kind %q", err, tt.kind)
			}
		})
	}
}

func TestValidatePrepareTopologyRejectsCrossRoleAliases(t *testing.T) {
	t.Run("candidate aliases health marker", func(t *testing.T) {
		store, sources := newStoreFixture(t, false)
		manifest := storeTestManifest(false)
		manifest.HealthURLFile = sources.Candidate
		err := ValidatePrepareTopology(store, sources, &manifest)
		var topologyErr *PathTopologyError
		if !errors.As(err, &topologyErr) || topologyErr.Kind != PathTopologyRuntimeAlias {
			t.Fatalf("error = %v, want runtime alias", err)
		}
	})

	t.Run("authority hardlinks wrapper", func(t *testing.T) {
		store, sources := newStoreFixture(t, false)
		manifest := storeTestManifest(false)
		manifest.WrapperPath = filepath.Join(t.TempDir(), "wrapper-hardlink")
		if err := os.Link(sources.Authority, manifest.WrapperPath); err != nil {
			t.Fatal(err)
		}
		err := ValidatePrepareTopology(store, sources, &manifest)
		var topologyErr *PathTopologyError
		if !errors.As(err, &topologyErr) || topologyErr.Kind != PathTopologyRuntimeAlias {
			t.Fatalf("error = %v, want runtime alias", err)
		}
	})

	t.Run("previous must be target", func(t *testing.T) {
		store, sources := newStoreFixture(t, true)
		manifest := storeTestManifest(true)
		manifest.TargetPath = sources.Previous
		manifest.TargetPath = writeFixtureFile(t, t.TempDir(), "different-target", "different")
		err := ValidatePrepareTopology(store, sources, &manifest)
		var topologyErr *PathTopologyError
		if !errors.As(err, &topologyErr) || topologyErr.Kind != PathTopologyPreviousTarget {
			t.Fatalf("error = %v, want previous-target mismatch", err)
		}
	})
}

func TestStorePrepareAndLoadEnforceOperationalTopology(t *testing.T) {
	t.Run("prepare", func(t *testing.T) {
		store, sources := newStoreFixture(t, false)
		locked, err := store.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		manifest := storeTestManifest(false)
		manifest.HealthURLFile = manifest.TargetPath
		if _, err := locked.Prepare(manifest, sources); !errors.Is(err, ErrStateMalformed) {
			t.Fatalf("Prepare error = %v, want ErrStateMalformed", err)
		}
		if loaded, err := locked.Load(); err != nil || loaded != nil {
			t.Fatalf("invalid topology published active state: %#v, %v", loaded, err)
		}
	})

	t.Run("prepare cross-role", func(t *testing.T) {
		store, sources := newStoreFixture(t, false)
		locked, err := store.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		manifest := storeTestManifest(false)
		manifest.HealthURLFile = sources.Candidate
		if _, err := locked.Prepare(manifest, sources); !errors.Is(err, ErrStateMalformed) {
			t.Fatalf("Prepare error = %v, want ErrStateMalformed", err)
		}
		if loaded, err := locked.Load(); err != nil || loaded != nil {
			t.Fatalf("cross-role alias published active state: %#v, %v", loaded, err)
		}
	})

	t.Run("load", func(t *testing.T) {
		store, sources := newStoreFixture(t, false)
		locked, err := store.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		if _, err := locked.Prepare(storeTestManifest(false), sources); err != nil {
			t.Fatal(err)
		}
		updateManifestFile(t, store, func(raw map[string]any) {
			raw["health_url_file"] = raw["target_path"]
		})
		if _, err := locked.Load(); !errors.Is(err, ErrStateMalformed) {
			t.Fatalf("Load error = %v, want ErrStateMalformed", err)
		}
	})
}

func TestStoreHooksInjectFailuresAtNamedBoundaries(t *testing.T) {
	tests := []struct {
		point         StoreHookPoint
		activePresent bool
	}{
		{point: StoreBeforeManifestTempWrite},
		{point: StoreAfterManifestTempWrite},
		{point: StoreBeforeManifestTempSync},
		{point: StoreAfterManifestTempSync},
		{point: StoreBeforeManifestPublish},
		{point: StoreAfterManifestPublish},
		{point: StoreBeforeManifestDirSync},
		{point: StoreAfterManifestDirSync},
		{point: StoreBeforeActivePublish},
		{point: StoreAfterActivePublish, activePresent: true},
		{point: StoreBeforeActiveParentSync, activePresent: true},
		{point: StoreAfterActiveParentSync, activePresent: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.point), func(t *testing.T) {
			store, sources := newStoreFixture(t, false)
			injected := errors.New("injected")
			store.hook = func(point StoreHookPoint) error {
				if point == tt.point {
					return injected
				}
				return nil
			}
			locked, err := store.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Close()
			_, err = locked.Prepare(storeTestManifest(false), sources)
			if !errors.Is(err, injected) {
				t.Fatalf("Prepare error = %v, want injected", err)
			}
			store.hook = nil
			manifest, loadErr := locked.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if (manifest != nil) != tt.activePresent {
				t.Fatalf("active present = %v, want %v", manifest != nil, tt.activePresent)
			}
		})
	}
}

func TestStoreHooksBoundRewriteClearAndCleanup(t *testing.T) {
	store, sources := newStoreFixture(t, false)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	prepared, err := locked.Prepare(storeTestManifest(false), sources)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected")

	rewritePoints := []struct {
		point     StoreHookPoint
		wantState State
	}{
		{StoreBeforeManifestTempWrite, StatePrepared},
		{StoreAfterManifestTempWrite, StatePrepared},
		{StoreBeforeManifestTempSync, StatePrepared},
		{StoreAfterManifestTempSync, StatePrepared},
		{StoreBeforeManifestPublish, StatePrepared},
		{StoreAfterManifestPublish, StatePending},
		{StoreBeforeManifestDirSync, StatePending},
		{StoreAfterManifestDirSync, StatePending},
	}
	for _, tt := range rewritePoints {
		t.Run("rewrite_"+string(tt.point), func(t *testing.T) {
			rewriteStore, rewriteSources := newStoreFixture(t, false)
			rewriteLocked, acquireErr := rewriteStore.Acquire()
			if acquireErr != nil {
				t.Fatal(acquireErr)
			}
			defer rewriteLocked.Close()
			rewritePrepared, prepareErr := rewriteLocked.Prepare(storeTestManifest(false), rewriteSources)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			requested := *rewritePrepared
			requested.State = StatePending
			rewriteStore.hook = func(point StoreHookPoint) error {
				if point == tt.point {
					return injected
				}
				return nil
			}
			if err := rewriteLocked.Rewrite(requested); !errors.Is(err, injected) {
				t.Fatalf("Rewrite error = %v, want injected", err)
			}
			rewriteStore.hook = nil
			loaded, loadErr := rewriteLocked.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if loaded.State != tt.wantState {
				t.Fatalf("state = %q, want %q", loaded.State, tt.wantState)
			}
		})
	}

	orphan := filepath.Join(store.Root(), "cleanup.orphan")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	store.hook = func(point StoreHookPoint) error {
		if point == StoreBeforeOrphanCleanup {
			return injected
		}
		return nil
	}
	if err := locked.CleanupOrphans(); !errors.Is(err, injected) {
		t.Fatalf("CleanupOrphans error = %v, want injected", err)
	}
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatalf("failed cleanup removed orphan: %v", err)
	}

	store.hook = func(point StoreHookPoint) error {
		if point == StoreBeforeClearPublish {
			return injected
		}
		return nil
	}
	if _, err := locked.Clear(prepared.ID); !errors.Is(err, injected) {
		t.Fatalf("Clear error = %v, want injected", err)
	}
	if loaded, err := locked.Load(); err != nil || loaded == nil {
		t.Fatalf("failed clear removed active: %#v, %v", loaded, err)
	}

	store.hook = func(point StoreHookPoint) error {
		if point == StoreAfterClearPublish {
			return injected
		}
		return nil
	}
	if _, err := locked.Clear(prepared.ID); !errors.Is(err, injected) {
		t.Fatalf("post-clear hook error = %v, want injected", err)
	}
	store.hook = nil
	if loaded, err := locked.Load(); err != nil || loaded != nil {
		t.Fatalf("post-clear hook failure did not preserve clear point: %#v, %v", loaded, err)
	}

	for _, point := range []StoreHookPoint{
		StoreBeforeClearParentSync,
		StoreAfterClearParentSync,
		StoreBeforeClearCleanup,
		StoreAfterClearCleanup,
	} {
		t.Run("clear_warning_"+string(point), func(t *testing.T) {
			store, sources := newStoreFixture(t, false)
			locked, err := store.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Close()
			prepared, err := locked.Prepare(storeTestManifest(false), sources)
			if err != nil {
				t.Fatal(err)
			}
			store.hook = func(got StoreHookPoint) error {
				if got == point {
					return injected
				}
				return nil
			}
			warnings, clearErr := locked.Clear(prepared.ID)
			if clearErr != nil || len(warnings) != 1 || !errors.Is(warnings[0], injected) {
				t.Fatalf("Clear warnings=%v error=%v, want one injected warning", warnings, clearErr)
			}
			store.hook = nil
			if loaded, err := locked.Load(); err != nil || loaded != nil {
				t.Fatalf("warning boundary did not commit clear: %#v, %v", loaded, err)
			}
		})
	}
}

func TestValidLaunchAgentLabel(t *testing.T) {
	for _, label := range []string{"com.example.gateway", "gateway_1"} {
		if !ValidLaunchAgentLabel(label) {
			t.Fatalf("expected valid label %q", label)
		}
	}
	for _, label := range []string{"", ".", "..", "../gateway", "com..gateway", "com.gateway.plist/other", "-gateway", "gateway-", "gateway label"} {
		if ValidLaunchAgentLabel(label) {
			t.Fatalf("expected invalid label %q", label)
		}
	}
}

func TestStoreLoadRejectsMalformedStateWithoutCleaningEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, store *Store)
	}{
		{
			name: "unsupported version",
			mutate: func(t *testing.T, store *Store) {
				updateManifestFile(t, store, func(raw map[string]any) { raw["version"] = float64(ManifestVersion + 1) })
			},
		},
		{
			name: "unknown field",
			mutate: func(t *testing.T, store *Store) {
				updateManifestFile(t, store, func(raw map[string]any) { raw["surprise"] = "unsafe" })
			},
		},
		{
			name: "trailing json",
			mutate: func(t *testing.T, store *Store) {
				path := filepath.Join(store.Root(), activeDirName, manifestFileName)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("{}\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "candidate hash mismatch",
			mutate: func(t *testing.T, store *Store) {
				path := filepath.Join(store.Root(), activeDirName, candidateFileName)
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("tampered"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o500); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked authority",
			mutate: func(t *testing.T, store *Store) {
				path := filepath.Join(store.Root(), activeDirName, authorityFileName)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("candidate", path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest permissions",
			mutate: func(t *testing.T, store *Store) {
				if err := os.Chmod(filepath.Join(store.Root(), activeDirName, manifestFileName), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, sources := newStoreFixture(t, false)
			locked, err := store.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Close()
			if _, err := locked.Prepare(storeTestManifest(false), sources); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(store.Root(), activeDirName, ".manifest.keep-me.tmp")
			if err := os.WriteFile(orphan, []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, store)
			if _, err := locked.Load(); !errors.Is(err, ErrStateMalformed) {
				t.Fatalf("Load error = %v, want ErrStateMalformed", err)
			}
			if _, err := os.Lstat(orphan); err != nil {
				t.Fatalf("malformed load cleaned recovery evidence: %v", err)
			}
		})
	}
}

func TestStoreLockIsFailFastAndRejectsReplacement(t *testing.T) {
	store, _ := newStoreFixture(t, false)
	first, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := store.Acquire(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire error = %v, want ErrBusy", err)
	}

	lockPath := filepath.Join(store.Root(), lockFileName)
	if err := os.Rename(lockPath, lockPath+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Load(); err == nil || !strings.Contains(err.Error(), "lock inode changed") {
		t.Fatalf("Load after lock replacement error = %v", err)
	}
}

func TestStoreRejectsSymlinkedLockAndArtifactSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreAt(root, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, lockFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(); err == nil {
		t.Fatal("Acquire accepted symlinked permanent lock")
	}

	if err := os.Remove(filepath.Join(root, lockFileName)); err != nil {
		t.Fatal(err)
	}
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	sourceTarget := writeFixtureFile(t, t.TempDir(), "target", "candidate")
	sourceLink := filepath.Join(t.TempDir(), "candidate-link")
	if err := os.Symlink(sourceTarget, sourceLink); err != nil {
		t.Fatal(err)
	}
	authority := writeFixtureFile(t, t.TempDir(), "authority", "authority")
	_, err = locked.Prepare(storeTestManifest(false), ArtifactSources{Candidate: sourceLink, Authority: authority})
	if err == nil {
		t.Fatal("Prepare accepted symlinked artifact source")
	}
	if got, loadErr := locked.Load(); loadErr != nil || got != nil {
		t.Fatalf("failed prepare published state: %#v, %v", got, loadErr)
	}
}

func TestConcurrentPreparePublishesExactlyOneTransaction(t *testing.T) {
	store, sources := newStoreFixture(t, false)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			locked, err := store.Acquire()
			if err != nil {
				errs <- err
				return
			}
			defer locked.Close()
			_, err = locked.Prepare(storeTestManifest(false), sources)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrStateConflict) {
			t.Fatalf("contender error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful prepares = %d, want exactly 1", successes)
	}
}

func newStoreFixture(t *testing.T, withPrevious bool) (*Store, ArtifactSources) {
	t.Helper()
	store, err := NewStoreAt(filepath.Join(t.TempDir(), "state"), os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	sources := ArtifactSources{
		Candidate: writeFixtureFile(t, sourceDir, "candidate-source", "candidate bytes"),
		Authority: writeFixtureFile(t, sourceDir, "authority-source", "authority bytes"),
	}
	if withPrevious {
		sources.Previous = writeFixtureFile(t, sourceDir, "previous-source", "previous bytes")
	}
	return store, sources
}

func storeTestManifest(withPrevious bool) Manifest {
	manifest := Manifest{
		State:                 StatePrepared,
		ID:                    ReleaseID(strings.Repeat("a", 64)),
		Commit:                strings.Repeat("b", 40),
		TargetPath:            "/tmp/personal-mcp-gateway",
		PreviousPresent:       withPrevious,
		CandidateFile:         candidateFileName,
		CandidateSHA256:       storeHashText("candidate bytes"),
		AuthorityFile:         authorityFileName,
		AuthoritySHA256:       storeHashText("authority bytes"),
		LaunchAgentLabel:      "com.example.personal-mcp-gateway",
		PlistPath:             "/tmp/com.example.personal-mcp-gateway.plist",
		PlistSHA256:           strings.Repeat("c", 64),
		WrapperPath:           "/tmp/personal-mcp-gateway-wrapper",
		WrapperSHA256:         strings.Repeat("d", 64),
		MCPWrapperPath:        "/tmp/personal-mcp-gateway-mcp-wrapper",
		MCPWrapperSHA256:      strings.Repeat("e", 64),
		StdoutPath:            "/tmp/personal-mcp-gateway.stdout.log",
		StderrPath:            "/tmp/personal-mcp-gateway.stderr.log",
		EnvironmentPath:       "/tmp/personal-mcp-gateway.env",
		EnvironmentSHA256:     strings.Repeat("f", 64),
		HealthURLFile:         "/tmp/personal-mcp-gateway.health-url",
		ReadyTimeoutSeconds:   15,
		ReadyPollMilliseconds: 250,
	}
	if withPrevious {
		manifest.PreviousFile = previousFileName
		manifest.PreviousSHA256 = storeHashText("previous bytes")
	}
	return manifest
}

func storeHashText(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func writeFixtureFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists (error %v)", path, err)
	}
}

func updateManifestFile(t *testing.T, store *Store, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(store.Root(), activeDirName, manifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	mutate(raw)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
