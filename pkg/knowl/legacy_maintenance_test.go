package knowl

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const legacyUncommittedMode = "uncommitted"
const legacyManifestMode = "manifest"

func TestLegacyCommittedOrdinaryUpgradeFinishesOutcome(t *testing.T) {
	for _, state := range []domain.SourceLifecycleState{domain.SourceLifecycleActive, domain.SourceLifecycleRetiring, domain.SourceLifecycleDeleted} {
		t.Run(string(state), func(t *testing.T) {
			content, plan, reader := ordinaryAuthorityFixture(t)
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "operations.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			accepted, err := content.ResolveAcceptedSources(t.Context(), testScope, plan.SourceRefs)
			if err != nil {
				t.Fatal(err)
			}
			source := accepted[plan.SourceRefs[0]]
			schema, err := content.Schema(t.Context(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			generation := strings.Repeat("c", 64)
			key := domain.OperationKey{Scope: testScope, Source: source.Source, Version: source.Version, MaintenanceGeneration: generation}
			reservation, err := store.Reserve(t.Context(), key, domain.OperationMeta{Key: key, AcceptedSource: source, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation, CreatedAt: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			plan.OperationID = string(reservation.ID)
			legacy, err := contentfs.New(content.Root())
			if err != nil {
				t.Fatal(err)
			}
			staged, err := legacy.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.Commit(t.Context(), staged); err != nil {
				t.Fatal(err)
			}
			// Old ordinary commits removed their journal and never emitted this receipt.
			receiptKey := "maintainer\x00" + string(testScope) + "\x00" + plan.OperationID
			receiptToken := fmt.Sprintf("%x", sha256.Sum256([]byte(receiptKey)))
			if err := os.Remove(filepath.Join(content.Root(), ".knowl", "commits", receiptToken[:32]+".yaml")); err != nil {
				t.Fatal(err)
			}
			for owner, current := range reader.states {
				current.State = state
				reader.states[owner] = current
			}
			pageBefore := mustReadHierarchyFile(t, content, plan.Edits[0].Path)
			logBefore := mustReadHierarchyFile(t, content, "wiki/log.md")
			service, err := app.NewIngestService(content, store, store, unexpectedMaintenancePlanner{t: t}, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimReady(t.Context(), testScope, domain.WorkLease{Token: "legacy-upgrade", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.RunToTerminal(t.Context(), claim)
			if err != nil {
				t.Fatalf("already committed legacy canonical work cannot finish projection/outcome: %v", err)
			}
			operation, err := store.Operation(t.Context(), testScope, reservation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if operation.Status != domain.StatusCommitted {
				t.Fatalf("operation status=%s", operation.Status)
			}

			projected, err := store.Search(t.Context(), testScope, "Late", app.DefaultReadLimits(), nil)
			if err != nil || len(projected) != 1 || projected[0].ID != "entities/late" {
				t.Fatalf("projection=%#v,%v", projected, err)
			}
			if _, err := service.Apply(t.Context(), testScope, reservation.ID); err != nil {
				t.Fatal(err)
			}
			if after := mustReadHierarchyFile(t, content, plan.Edits[0].Path); !bytes.Equal(after, pageBefore) {
				t.Fatal("legacy replay changed page")
			}
			if after := mustReadHierarchyFile(t, content, "wiki/log.md"); !bytes.Equal(after, logBefore) {
				t.Fatal("legacy replay duplicated log")
			}
		})
	}
}

func TestLegacyUncommittedOrTamperedStageCannotClaimPublication(t *testing.T) {
	for _, mode := range []string{legacyUncommittedMode, "page", "log", "stage", legacyManifestMode, "preimage"} {
		t.Run(mode, func(t *testing.T) {
			content, plan, _ := ordinaryAuthorityFixture(t)
			legacy, err := contentfs.New(content.Root())
			if err != nil {
				t.Fatal(err)
			}
			staged, err := legacy.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if mode != legacyUncommittedMode {
				if _, err := legacy.Commit(t.Context(), staged); err != nil {
					t.Fatal(err)
				}
				key := "maintainer\x00" + string(testScope) + "\x00" + plan.OperationID
				token := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
				if err := os.Remove(filepath.Join(content.Root(), ".knowl", "commits", token[:32]+".yaml")); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "page":
				writeMaintenanceFixture(t, content, plan.Edits[0].Path, append(plan.Edits[0].Content, []byte("\nchanged\n")...))
			case "log":
				writeMaintenanceFixture(t, content, "wiki/log.md", append(mustReadHierarchyFile(t, content, "wiki/log.md"), '\n'))
			case "stage":
				token := digestBytes([]byte(plan.OperationID))
				writeMaintenanceFixture(t, content, filepath.Join(".knowl", "staging", token[:32], plan.Edits[0].Path), []byte("changed staged content"))
			case legacyManifestMode, "preimage":
				token := digestBytes([]byte(plan.OperationID))
				path := filepath.Join(".knowl", "staging", token[:32], "manifest.yaml")
				var manifest map[string]any
				if err := yaml.Unmarshal(mustReadHierarchyFile(t, content, path), &manifest); err != nil {
					t.Fatal(err)
				}
				if mode == legacyManifestMode {
					manifest["source_refs"] = append(append([]string(nil), plan.SourceRefs...), "git:tampered/doc.md@v1")
				} else {
					manifest["log_expected_digest"] = strings.Repeat("f", 64)
				}
				encoded, err := yaml.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				writeMaintenanceFixture(t, content, path, encoded)
				staged.Digest = ""
			}
			logBefore := mustReadHierarchyFile(t, content, "wiki/log.md")
			if _, err := content.Commit(t.Context(), staged); !errors.Is(err, contentfs.ErrPlanConflict) {
				t.Fatalf("%s legacy proof error=%v", mode, err)
			}
			if after := mustReadHierarchyFile(t, content, "wiki/log.md"); !bytes.Equal(after, logBefore) {
				t.Fatal("rejected legacy proof mutated log")
			}
			if mode == legacyUncommittedMode {
				if _, err := os.Stat(filepath.Join(content.Root(), plan.Edits[0].Path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("precommit stage published: %v", err)
				}
			}
		})
	}
}
