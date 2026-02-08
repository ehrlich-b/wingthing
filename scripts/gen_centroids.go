// +build ignore

// Generates centroid embedding cache for spaces.yaml.
// Usage: go run scripts/gen_centroids.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/embedding"
)

func main() {
	root := "."
	yamlPath := filepath.Join(root, "spaces.yaml")
	cacheDir := filepath.Join(root, "spaces", "cache")

	emb, err := embedding.NewFromProvider("auto", "", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("embedder: %s\n", emb.Name())
	fmt.Printf("spaces:   %s\n", yamlPath)
	fmt.Printf("cache:    %s\n", cacheDir)

	start := time.Now()
	idx, err := embedding.LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(start)
	fmt.Printf("done:     %d spaces, %d embedders, %s\n", len(idx.Spaces), len(idx.EmbedderNames()), elapsed.Round(time.Millisecond))
}
