// Command knowl-searchtome evaluates the real Knowl retrieval pipeline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	st "github.com/baldaworks/knowl/pkg/knowl/store/eval/searchtome"
)

func git(args ...string) (string, error) {
	b, e := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(b)), e
}
func cpu() string {
	b, e := os.ReadFile("/proc/cpuinfo")
	if e != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}
func validName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
func ownedBase(repo string) (string, error) {
	base := filepath.Join(repo, ".searchtome-benchmark")
	err := os.Mkdir(base, 0700)
	if err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid benchmark directory")
	}
	return base, nil
}
func safeDataset(base, name string) (string, error) {
	if !validName(name) {
		return "", errors.New("invalid dataset directory")
	}
	root := filepath.Join(base, name)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid dataset directory")
	}
	for _, domain := range st.Domains {
		dir := filepath.Join(root, domain)
		info, err = os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("invalid dataset domain")
		}
		for _, split := range []string{"corpus", "train", "dev", "test"} {
			info, err = os.Lstat(filepath.Join(dir, split+".jsonl"))
			if err != nil || !info.Mode().IsRegular() {
				return "", errors.New("invalid dataset file")
			}
		}
	}
	return root, nil
}
func run() error {
	datasetName := flag.String("dataset", "dataset", "dataset directory name under .searchtome-benchmark")
	runName := flag.String("run", "", "new run directory name")
	endpoint := flag.String("tei", "http://127.0.0.1:8080/v1/embeddings", "local TEI endpoint")
	model := flag.String("model", "e5", "embedding model: e5 or gemma2")
	limit := flag.Int("limit", 0, "queries per domain for smoke; 0 runs all")
	flag.Parse()
	space := st.ReferenceSpace
	if *model == "gemma2" {
		space = st.Gemma2Space
	} else if *model != "e5" {
		return errors.New("unsupported embedding model")
	}
	if !validName(*runName) || !validName(*datasetName) || *runName == *datasetName || *limit < 0 {
		return errors.New("invalid run options")
	}
	if !st.LocalEndpoint(*endpoint) {
		return st.ErrTEI
	}
	repo, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("repository unavailable")
	}
	base, err := ownedBase(repo)
	if err != nil {
		return err
	}
	dataPath, err := safeDataset(base, *datasetName)
	if err != nil {
		return err
	}
	dataset, err := st.LoadDataset(dataPath)
	if err != nil {
		return errors.New("dataset verification failed")
	}
	ctx := context.Background()
	embedding, info, err := st.LocalEmbeddingServer(ctx, *endpoint, space)
	if err != nil {
		return st.ErrTEI
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return errors.New("commit unavailable")
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return errors.New("tree state unavailable")
	}
	tree := "clean"
	if status != "" {
		tree = "dirty"
	}
	runPath := filepath.Join(base, *runName)
	if err = os.Mkdir(runPath, 0700); err != nil {
		return errors.New("run directory already exists or unavailable")
	}
	env := st.Environment{Commit: commit, TreeState: tree, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPU: cpu(), Backend: "sqlite", Model: space, TEI: info, FailurePolicy: "lexical", K: 3, EmbeddingBatchInputs: 4, Characters: 1024, SourceFilter: "all", QueryLimit: *limit, RunKind: "full", Warmup: "none", Commands: []string{"knowl-searchtome --dataset " + *datasetName + " --run " + *runName + " --tei " + *endpoint + " --model " + *model + " --limit " + strconv.Itoa(*limit)}}
	if *limit > 0 {
		env.RunKind = "smoke"
	}
	projections := map[string]map[string]st.Projection{}
	var lexical, hybrid []st.Observation
	for _, domain := range st.Domains {
		fmt.Fprintf(os.Stderr, "Preparing %s\n", domain)
		published, err := st.Publish(ctx, filepath.Join(runPath, domain+"-workspace"), domain, dataset.Corpus[domain])
		if err != nil {
			return errors.New("corpus publication failed")
		}
		queries := append([]st.Query(nil), dataset.Queries[domain]...)
		if *limit > 0 && len(queries) > *limit {
			for i := *limit; i < len(queries); i++ {
				if queries[i].Exclusion == "" {
					queries[i].Exclusion = "smoke_limit"
				}
			}
		}
		lp, lo, err := st.RunMode(ctx, filepath.Join(runPath, domain+"-lexical.sqlite"), domain, published, queries, nil)
		if err != nil {
			return errors.New("lexical projection failed")
		}
		hp, ho, err := st.RunModeWithSpace(ctx, filepath.Join(runPath, domain+"-hybrid.sqlite"), domain, published, queries, embedding, space)
		if err != nil {
			return errors.New("hybrid projection failed")
		}
		projections[domain] = map[string]st.Projection{"lexical": lp, "hybrid": hp}
		lexical = append(lexical, lo...)
		hybrid = append(hybrid, ho...)
	}
	report := st.NewReport(env, dataset, projections, lexical, hybrid)
	if err = st.WriteReport(runPath, report); err != nil {
		return errors.New("report publication failed")
	}
	fmt.Println("Completed:", runPath)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
