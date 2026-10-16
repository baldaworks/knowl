package searchtest

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
)

// Validate the persisted measurements as typed data, never prose substrings.
func TestVectorEvaluationResults(t *testing.T) {
	file, err := os.Open("../../../../../docs/testing/native-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var artifact struct {
		SchemaVersion                                       int
		NativeRevision, LegacyRevision, Toolchain, Hardware string
		Commands                                            []string
		Observations                                        []VectorBenchmarkObservation
		Reproduction                                        []VectorBenchmarkObservation
		Plans                                               []json.RawMessage
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 1 || artifact.NativeRevision == "" || artifact.LegacyRevision == "" || artifact.Toolchain == "" || artifact.Hardware == "" || len(artifact.Commands) == 0 || len(artifact.Plans) == 0 {
		t.Fatal("missing evaluation provenance")
	}
	if len(artifact.Reproduction) != 1 || artifact.Reproduction[0].Samples != 3 || len(artifact.Reproduction[0].WallNS) != 3 {
		t.Fatal("missing separate reproducibility sample")
	}
	for _, raw := range artifact.Plans {
		var plan struct {
			Backend, Mode, SourceLog string
			Case                     VectorBenchmarkCase
			Nodes                    []struct {
				Kind               string  `json:"Node Type"`
				Rows               float64 `json:"Actual Rows"`
				Loops              float64 `json:"Actual Loops"`
				ID, Parent, Unused int64
			}
			Plans []struct {
				Columns   []string
				Nodes     []struct{ ID, Parent, Unused int64 }
				Execution []struct {
					Plan struct {
						Kind string  `json:"Node Type"`
						Rows float64 `json:"Actual Rows"`
					}
					ExecutionMS float64 `json:"Execution Time"`
				}
			}
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		if plan.SourceLog == "" || plan.Case.Chunks < 1 {
			t.Fatal("missing plan provenance")
		}
		if plan.Mode != "" {
			if len(plan.Nodes) == 0 {
				t.Fatal("missing isolated plan nodes")
			}
			if plan.Backend == vectorBenchPostgres {
				want := float64(plan.Case.Chunks)
				if plan.Mode == "isolated-kernel-native-plan" {
					want = 20
				}
				if plan.Nodes[0].Kind == "" || plan.Nodes[0].Rows != want || plan.Nodes[0].Loops != 1 {
					t.Fatalf("invalid observed isolated root:%+v", plan.Nodes[0])
				}
			}
		} else {
			if len(plan.Plans) == 0 {
				t.Fatal("missing runtime plans")
			}
			for _, query := range plan.Plans {
				if len(query.Columns) == 0 {
					t.Fatal("missing observed result columns")
				}
				if plan.Backend == vectorBenchPostgres {
					if len(query.Execution) != 1 || query.Execution[0].Plan.Kind == "" || query.Execution[0].Plan.Rows < 0 || query.Execution[0].ExecutionMS < 0 {
						t.Fatal("invalid observed runtime executor")
					}
				} else if len(query.Nodes) == 0 {
					t.Fatal("missing SQLite runtime plan nodes")
				}
			}
		}
	}
	runtimePairs := make(map[string]map[string]VectorBenchmarkObservation)
	kernels := make(map[string]map[string]bool)
	capacity := make(map[string]map[int]bool)
	resources := make(map[string]bool)
	for _, o := range artifact.Observations {
		switch o.Outcome {
		case "supported":
			if o.Samples != 3 || len(o.WallNS) != 3 {
				t.Fatalf("case%+v samples=%d", o.Case, o.Samples)
			}
			ordered := slices.Clone(o.WallNS)
			slices.Sort(ordered)
			if ordered[0] <= 0 || o.MedianNS != float64(ordered[1]) || o.P95NS != float64(ordered[2]) || o.MaxNS != float64(ordered[2]) || o.BPerOp <= 0 || o.AllocsPerOp <= 0 {
				t.Fatalf("invalid measured distribution:%+v", o)
			}
			if o.Mode == "production-interface" {
				if o.Case.Chunks != 8192 || o.Retrieval == nil || o.Retrieval.ScannedChunks != 8192 || len(o.ResultIDs) != 5 {
					t.Fatalf("invalid production result:%+v", o)
				}
				key := o.Backend + "/" + o.Case.Name()
				if runtimePairs[key] == nil {
					runtimePairs[key] = make(map[string]VectorBenchmarkObservation)
				}
				kind := "legacy"
				if o.NativeOracleChecked {
					kind = "native"
					if o.Transfer.VectorRows != 0 || o.Transfer.VectorBytes != 0 || o.Transfer.CandidateRows != 60 || o.Transfer.EvidenceRows != 15 {
						t.Fatalf("invalid native transfer:%+v", o.Transfer)
					}
				} else if o.Transfer.VectorRows != 8192*3 || o.Transfer.VectorBytes != int64(8192*o.Case.Dimensions*4*3) {
					t.Fatalf("invalid legacy transfer:%+v", o.Transfer)
				}
				runtimePairs[key][kind] = o
			} else {
				if o.Mode != "isolated-kernel-native-sql" && o.Mode != "isolated-kernel-legacy-full-transfer" {
					t.Fatalf("unknown supported mode:%s", o.Mode)
				}
				key := o.Backend + "/" + o.Case.Name()
				if kernels[key] == nil {
					kernels[key] = make(map[string]bool)
				}
				kernels[key][o.Mode] = true
				if o.NativeOracleChecked && (o.Transfer.VectorBytes != 0 || o.Transfer.CandidateRows != 60) {
					t.Fatalf("invalid kernel transfer:%+v", o.Transfer)
				}
				if !o.NativeOracleChecked && (o.Transfer.VectorRows != int64(o.Case.Chunks*3) || o.Transfer.VectorBytes != int64(o.Case.Chunks)*int64(o.Case.Dimensions)*4*3) {
					t.Fatalf("invalid isolated legacy transfer:%+v", o.Transfer)
				}
			}
			if o.NativeOracleChecked {
				u := math.Ldexp(1, -24)
				n := float64(o.Case.Dimensions + 2)
				if o.MathMaxAbsError < 0 || o.MathMaxAbsError > 8*n*u/(1-n*u)+8*u {
					t.Fatalf("numerical bound:%+v", o)
				}
			}
		case "capacity_rejected":
			if o.Reason != "projection_capacity" || o.Samples != 0 {
				t.Fatalf("invalid capacity:%+v", o)
			}
			if capacity[o.Backend] == nil {
				capacity[o.Backend] = make(map[int]bool)
			}
			capacity[o.Backend][o.Case.Chunks] = true
		case "resource_limited":
			if o.Samples != 0 || VectorBenchmarkResourceEstimate(o.Case) <= 512<<20 {
				t.Fatalf("invalid resource outcome:%+v", o)
			}
			resources[o.Backend] = true
		default:
			t.Fatalf("unknown outcome:%s", o.Outcome)
		}
	}
	if len(runtimePairs) != 10 {
		t.Fatalf("runtime pair count=%d want10", len(runtimePairs))
	}
	for key, pair := range runtimePairs {
		if len(pair) != 2 || pair["native"].Checksum != pair["legacy"].Checksum {
			t.Fatalf("unmatched fixture:%s", key)
		}
	}
	for key, pair := range kernels {
		if !pair["isolated-kernel-native-sql"] || !pair["isolated-kernel-legacy-full-transfer"] {
			t.Fatalf("unmatched kernel:%s", key)
		}
	}
	for _, backend := range []string{"sqlite", vectorBenchPostgres} {
		if !capacity[backend][8192] || !capacity[backend][25000] || !capacity[backend][100000] || !resources[backend] {
			t.Fatalf("missing unsupported outcome:%s", backend)
		}
		c := VectorBenchmarkCase{100000, 2, 4, 50}
		if len(kernels[backend+"/"+c.Name()]) != 2 {
			t.Fatalf("missing actual large isolated comparison:%s", backend)
		}
	}
}
