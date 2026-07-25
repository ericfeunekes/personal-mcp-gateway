//go:build darwin

package fsx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMutationStatMoveDeleteFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Notes", "one.md"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	ctx := context.Background()
	target, err := mutator.StatMutationTarget(ctx, ".", "notes/ONE.md")
	if err != nil {
		t.Fatal(err)
	}
	if target.Resolved.Rel != "Notes/one.md" || target.Resolved.Kind != KindFile {
		t.Fatalf("target = %+v", target)
	}
	if _, err := mutator.Move(ctx, ".", "Notes/one.md", "Notes/two.md", SourceFingerprint{}); !IsCode(err, CodeSourceChanged) {
		t.Fatalf("stale move err = %v", err)
	}
	result, err := mutator.Move(ctx, ".", "Notes/one.md", "Notes/two.md", target.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved.Rel != "Notes/two.md" || result.Fingerprint == (SourceFingerprint{}) {
		t.Fatalf("move result = %+v", result)
	}
	movedStat, err := mutator.StatMutationTarget(ctx, ".", "Notes/two.md")
	if err != nil || movedStat != (MutationTarget{Resolved: result.Resolved, Fingerprint: result.Fingerprint}) {
		t.Fatalf("move follow-up stat = %+v, %v; result = %+v", movedStat, err, result)
	}
	if _, err := os.Stat(filepath.Join(root, "Notes", "one.md")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "Notes", "two.md")); err != nil || string(got) != "one" {
		t.Fatalf("destination = %q, %v", got, err)
	}
	if _, err := mutator.Delete(ctx, ".", "Notes/two.md", target.Fingerprint); !IsCode(err, CodeSourceChanged) {
		t.Fatalf("old-path fingerprint delete err = %v", err)
	}
	deleted, err := mutator.Delete(ctx, ".", "Notes/two.md", result.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Resolved.Exists || deleted.Resolved.Rel != "Notes/two.md" {
		t.Fatalf("delete result = %+v", deleted)
	}
}

func TestMutationMoveUnsupportedFailsClosed(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.md")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, "unused")
	target, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
	if err != nil {
		t.Fatal(err)
	}
	previous := mutationRenameExclusive
	mutationRenameExclusive = func(int, string, int, string) error { return unix.ENOTSUP }
	defer func() { mutationRenameExclusive = previous }()
	if _, err := mutator.Move(context.Background(), ".", "source.md", "destination.md", target.Fingerprint); !IsCode(err, CodeUnsupported) {
		t.Fatalf("move err = %v", err)
	}
	assertMutationFileContent(t, sourcePath, "source")
	if _, err := os.Stat(filepath.Join(root, "destination.md")); !os.IsNotExist(err) {
		t.Fatalf("destination exists: %v", err)
	}
}

func TestMutationMoveHostileDestinationCollisionAtExclusiveRename(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.md")
	destinationPath := filepath.Join(root, "destination.md")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	target, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
	if err != nil {
		t.Fatal(err)
	}
	vault.testHooks = &vaultTestHooks{beforeMutationEffect: func() {
		if err := os.WriteFile(destinationPath, []byte("external-writer"), 0o600); err != nil {
			t.Errorf("destination writer: %v", err)
		}
	}}
	if _, err := mutator.Move(context.Background(), ".", "source.md", "destination.md", target.Fingerprint); !IsCode(err, CodeDestinationExists) {
		t.Fatalf("move collision err = %v", err)
	}
	assertMutationFileContent(t, sourcePath, "source")
	assertMutationFileContent(t, destinationPath, "external-writer")
}

func TestMutationDirectFileFinalRevalidationRefusesObservableChanges(t *testing.T) {
	for _, operation := range []string{"move", "delete"} {
		for _, change := range []string{"same-inode-rewrite", "name-replacement"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				root := t.TempDir()
				sourcePath := filepath.Join(root, "source.md")
				displacedPath := filepath.Join(root, "displaced.md")
				destinationPath := filepath.Join(root, "destination.md")
				nonTargetPath := filepath.Join(root, "non-target.md")
				if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(nonTargetPath, []byte("non-target"), 0o600); err != nil {
					t.Fatal(err)
				}
				vault, err := NewVault(root)
				if err != nil {
					t.Fatal(err)
				}
				mutator := NewMutator(vault)
				target, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
				if err != nil {
					t.Fatal(err)
				}
				vault.testHooks = &vaultTestHooks{beforeMutationFinal: func() {
					if change == "same-inode-rewrite" {
						if err := os.WriteFile(sourcePath, []byte("external-rewrite"), 0o600); err != nil {
							t.Errorf("rewrite: %v", err)
						}
						return
					}
					if err := os.Rename(sourcePath, displacedPath); err != nil {
						t.Errorf("displace source: %v", err)
						return
					}
					if err := os.WriteFile(sourcePath, []byte("external-name"), 0o600); err != nil {
						t.Errorf("replace source name: %v", err)
					}
				}}
				if operation == "move" {
					_, err = mutator.Move(context.Background(), ".", "source.md", "destination.md", target.Fingerprint)
				} else {
					_, err = mutator.Delete(context.Background(), ".", "source.md", target.Fingerprint)
				}
				if !IsCode(err, CodeSourceChanged) {
					t.Fatalf("effect err = %v", err)
				}
				if _, err := os.Stat(destinationPath); !os.IsNotExist(err) {
					t.Fatalf("destination changed: %v", err)
				}
				assertMutationFileContent(t, nonTargetPath, "non-target")
				if change == "same-inode-rewrite" {
					assertMutationFileContent(t, sourcePath, "external-rewrite")
				} else {
					assertMutationFileContent(t, sourcePath, "external-name")
					assertMutationFileContent(t, displacedPath, "source")
				}
			})
		}
	}
}

func TestMutationDirectAcceptedPostRevalidationWindowIsTruthful(t *testing.T) {
	for _, operation := range []string{"move", "delete"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source.md")
			destinationPath := filepath.Join(root, "destination.md")
			if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			vault, err := NewVault(root)
			if err != nil {
				t.Fatal(err)
			}
			mutator := NewMutator(vault)
			target, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
			if err != nil {
				t.Fatal(err)
			}
			vault.testHooks = &vaultTestHooks{beforeMutationEffect: func() {
				if err := os.WriteFile(sourcePath, []byte("accepted-window"), 0o600); err != nil {
					t.Errorf("window writer: %v", err)
				}
			}}
			if operation == "move" {
				if _, err := mutator.Move(context.Background(), ".", "source.md", "destination.md", target.Fingerprint); err != nil {
					t.Fatal(err)
				}
				assertMutationFileContent(t, destinationPath, "accepted-window")
			} else {
				if _, err := mutator.Delete(context.Background(), ".", "source.md", target.Fingerprint); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
				t.Fatalf("source remains: %v", err)
			}
		})
	}
}

func TestMutationEmptyDirectoryAndDestinationRefusal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nonempty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nonempty", "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "collision"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	ctx := context.Background()
	if _, err := mutator.StatMutationTarget(ctx, ".", "nonempty"); !IsCode(err, CodeNotEmpty) {
		t.Fatalf("nonempty stat err = %v", err)
	}
	target, err := mutator.StatMutationTarget(ctx, ".", "empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.Move(ctx, ".", "empty", "collision", target.Fingerprint); !IsCode(err, CodeDestinationExists) {
		t.Fatalf("collision err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "empty")); err != nil {
		t.Fatalf("source changed after refusal: %v", err)
	}
	if _, err := mutator.Delete(ctx, ".", "empty", target.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestMutationConfinementAndSymlinkRefusal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	for _, path := range []string{"../secret", "/etc/passwd", ".hidden"} {
		if _, err := mutator.StatMutationTarget(context.Background(), ".", path); !IsCode(err, CodePathDenied) {
			t.Fatalf("path %q err = %v", path, err)
		}
	}
	if _, err := mutator.StatMutationTarget(context.Background(), ".", "link"); !IsCode(err, CodeSymlinkDenied) {
		t.Fatalf("symlink err = %v", err)
	}
}

func TestMutationAmbiguousCanonicalNameFailsClosed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Case.md", "case.md", "source.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Skip("fixture volume is case-insensitive; case-sensitive collision boundary is unavailable")
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	if _, err := mutator.StatMutationTarget(context.Background(), ".", "cASE.MD"); !IsCode(err, CodeSourceChanged) {
		t.Fatalf("ambiguous stat err = %v", err)
	}
	source, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.Move(context.Background(), ".", "source.md", "cASE.MD", source.Fingerprint); !IsCode(err, CodeDestinationExists) {
		t.Fatalf("ambiguous destination err = %v", err)
	}
}

func TestClassifyMutationNamesDistinguishesAbsenceAndAmbiguity(t *testing.T) {
	tests := []struct {
		name   string
		caller string
		stored []string
		state  mutationNameState
	}{
		{name: "exact", caller: "Note.md", stored: []string{"note.md", "Note.md"}, state: mutationNameExact},
		{name: "unique nfc", caller: "Caf\u00e9.md", stored: []string{"Cafe\u0301.md"}, state: mutationNameUniqueNFC},
		{name: "unique fold", caller: "NOTE.md", stored: []string{"note.md"}, state: mutationNameUniqueFold},
		{name: "ambiguous fold", caller: "nOtE.md", stored: []string{"Note.md", "note.md"}, state: mutationNameAmbiguous},
		{name: "absent", caller: "missing.md", stored: []string{"other.md"}, state: mutationNameAbsent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyMutationNames(test.caller, test.stored); got.state != test.state {
				t.Fatalf("match = %+v, want state %v", got, test.state)
			}
		})
	}
}

func TestMutationFileFingerprintMatchesReadFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := NewMutator(vault).StatMutationTarget(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	file, err := vault.OpenFile(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if mutation.Fingerprint != file.Fingerprint() {
		t.Fatal("mutation stat and read produced different file fingerprints")
	}
}

func TestMutationEmptyDirectoryMembershipRaceRefuses(t *testing.T) {
	for _, test := range []struct {
		name string
		hook func(string) error
		code Code
	}{
		{name: "add", hook: func(dir string) error { return os.WriteFile(filepath.Join(dir, "child"), []byte("x"), 0o600) }, code: CodeSourceChanged},
		{name: "add-remove", hook: func(dir string) error {
			child := filepath.Join(dir, "child")
			if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
				return err
			}
			return os.Remove(child)
		}, code: CodeSourceChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "empty")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			vault, err := NewVault(root)
			if err != nil {
				t.Fatal(err)
			}
			target, err := NewMutator(vault).StatMutationTarget(context.Background(), ".", "empty")
			if err != nil {
				t.Fatal(err)
			}
			vault.testHooks = &vaultTestHooks{beforeMutationFinal: func() {
				if err := test.hook(dir); err != nil {
					t.Errorf("race hook: %v", err)
				}
			}}
			if _, err := NewMutator(vault).Delete(context.Background(), ".", "empty", target.Fingerprint); !IsCode(err, test.code) {
				t.Fatalf("delete err = %v, want %s", err, test.code)
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("directory changed after refusal: %v", err)
			}
		})
	}
}
