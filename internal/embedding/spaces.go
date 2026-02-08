package embedding

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Space is a single entry from spaces.yaml.
type Space struct {
	Slug        string `yaml:"slug"`
	Description string `yaml:"description"`
	Centroid    string `yaml:"centroid"`
}

// SpaceIndex holds parsed spaces with centroid embeddings for multiple embedders.
type SpaceIndex struct {
	Spaces []Space
	vecs   map[string][][]float32 // embedder name -> vectors parallel to Spaces
}

// LoadSpaces parses a spaces.yaml file.
func LoadSpaces(path string) ([]Space, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spaces yaml: %w", err)
	}
	var spaces []Space
	if err := yaml.Unmarshal(data, &spaces); err != nil {
		return nil, fmt.Errorf("parse spaces yaml: %w", err)
	}
	return spaces, nil
}

// LoadSpaceIndex loads spaces.yaml and embeds centroids with every provided embedder.
// Each embedder's vectors are cached independently in cacheDir as "{name}.bin".
func LoadSpaceIndex(yamlPath, cacheDir string, embedders ...Embedder) (*SpaceIndex, error) {
	spaces, err := LoadSpaces(yamlPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	// Extract centroid texts once
	texts := make([]string, len(spaces))
	for i, s := range spaces {
		texts[i] = s.Centroid
	}

	idx := &SpaceIndex{
		Spaces: spaces,
		vecs:   make(map[string][][]float32, len(embedders)),
	}

	for _, emb := range embedders {
		name := emb.Name()
		cachePath := filepath.Join(cacheDir, name+".bin")

		// Try cache first
		vecs, err := loadCache(cachePath, len(spaces), emb.Dims())
		if err == nil {
			idx.vecs[name] = vecs
			continue
		}

		// Embed all centroids
		vecs, err = embedBatched(emb, texts, 20)
		if err != nil {
			return nil, fmt.Errorf("embed centroids (%s): %w", name, err)
		}

		idx.vecs[name] = vecs

		if err := saveCache(cachePath, vecs); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to cache %s embeddings: %v\n", name, err)
		}
	}

	return idx, nil
}

// Vecs returns the centroid vectors for a given embedder name.
func (idx *SpaceIndex) Vecs(embedderName string) [][]float32 {
	return idx.vecs[embedderName]
}

// Lookup returns the Space and its vector for a given slug and embedder.
func (idx *SpaceIndex) Lookup(slug, embedderName string) (*Space, []float32) {
	vecs := idx.vecs[embedderName]
	for i, s := range idx.Spaces {
		if s.Slug == slug {
			var v []float32
			if vecs != nil && i < len(vecs) {
				v = vecs[i]
			}
			return &idx.Spaces[i], v
		}
	}
	return nil, nil
}

// EmbedderNames returns all loaded embedder names.
func (idx *SpaceIndex) EmbedderNames() []string {
	names := make([]string, 0, len(idx.vecs))
	for name := range idx.vecs {
		names = append(names, name)
	}
	return names
}

func embedBatched(emb Embedder, texts []string, batchSize int) ([][]float32, error) {
	all := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := emb.Embed(texts[i:end])
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		all = append(all, batch...)
	}
	return all, nil
}

func saveCache(path string, vecs [][]float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	count := uint32(len(vecs))
	dims := uint32(0)
	if len(vecs) > 0 {
		dims = uint32(len(vecs[0]))
	}
	if err := binary.Write(f, binary.LittleEndian, count); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, dims); err != nil {
		return err
	}
	for _, v := range vecs {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return nil
}

func loadCache(path string, expectedCount, expectedDims int) ([][]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var count, dims uint32
	if err := binary.Read(f, binary.LittleEndian, &count); err != nil {
		return nil, err
	}
	if err := binary.Read(f, binary.LittleEndian, &dims); err != nil {
		return nil, err
	}
	if int(count) != expectedCount || int(dims) != expectedDims {
		return nil, fmt.Errorf("cache mismatch: %dx%d vs expected %dx%d", count, dims, expectedCount, expectedDims)
	}

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	expectedSize := int64(8 + int(count)*int(dims)*4)
	if info.Size() != expectedSize {
		return nil, fmt.Errorf("cache size mismatch: %d vs expected %d", info.Size(), expectedSize)
	}

	vecs := make([][]float32, count)
	for i := range vecs {
		v := make([]float32, dims)
		if err := binary.Read(f, binary.LittleEndian, v); err != nil {
			return nil, err
		}
		vecs[i] = v
	}
	return vecs, nil
}

// VecAsBytes converts a float32 vector to a raw byte blob (for DB storage).
func VecAsBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}
