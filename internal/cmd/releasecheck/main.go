package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baldaworks/knowl/internal/releasecheck"
)

func main() {
	root := flag.String("root", ".", "repository root")
	version := flag.String("version", "", "expected stable release version or tag")
	binary := flag.String("binary", "", "optional release-shaped Knowl binary")
	stagedNPM := flag.String("staged-npm", "", "optional staged npm package root")
	flag.Parse()

	if err := releasecheck.Check(releasecheck.Options{
		Root:      *root,
		Version:   *version,
		Binary:    *binary,
		StagedNPM: *stagedNPM,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "release contract: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("release contract verified for %s\n", *version)
}
