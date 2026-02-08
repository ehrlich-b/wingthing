package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/embedding"
	"github.com/spf13/cobra"
)

func embedCmd() *cobra.Command {
	var textFlag string
	var modelFlag string
	var baseURLFlag string
	var formatFlag string

	cmd := &cobra.Command{
		Use:   "embed [text]",
		Short: "Generate embeddings using local Ollama",
		Long:  "Embed text into vectors via Ollama. Accepts text as argument, -t flag, or stdin (one text per line).",
		RunE: func(cmd *cobra.Command, args []string) error {
			emb := embedding.NewOllama(modelFlag, baseURLFlag)

			// Collect texts: arg > flag > stdin
			var texts []string
			if len(args) > 0 {
				texts = append(texts, strings.Join(args, " "))
			} else if textFlag != "" {
				texts = append(texts, textFlag)
			} else {
				stat, _ := os.Stdin.Stat()
				if (stat.Mode() & os.ModeCharDevice) == 0 {
					scanner := bufio.NewScanner(os.Stdin)
					scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
					for scanner.Scan() {
						line := strings.TrimSpace(scanner.Text())
						if line != "" {
							texts = append(texts, line)
						}
					}
					if err := scanner.Err(); err != nil {
						return fmt.Errorf("read stdin: %w", err)
					}
				}
			}

			if len(texts) == 0 {
				return fmt.Errorf("no text provided — use argument, -t flag, or pipe via stdin")
			}

			vecs, err := emb.Embed(texts)
			if err != nil {
				return fmt.Errorf("embed: %w", err)
			}

			switch formatFlag {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				for i, v := range vecs {
					out := embedResult{
						Text:      texts[i],
						Embedding: v,
						Dims:      len(v),
						Model:     emb.Name(),
					}
					if err := enc.Encode(out); err != nil {
						return fmt.Errorf("encode: %w", err)
					}
				}
			case "raw":
				for _, v := range vecs {
					os.Stdout.Write(embedding.VecAsBytes(v))
				}
			default:
				return fmt.Errorf("unknown format %q — use json or raw", formatFlag)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&textFlag, "text", "t", "", "text to embed")
	cmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Ollama model (default: nomic-embed-text)")
	cmd.Flags().StringVar(&baseURLFlag, "base-url", "", "Ollama base URL (default: http://localhost:11434)")
	cmd.Flags().StringVarP(&formatFlag, "format", "f", "json", "output format: json, raw")

	return cmd
}

type embedResult struct {
	Text      string    `json:"text"`
	Embedding []float32 `json:"embedding"`
	Dims      int       `json:"dims"`
	Model     string    `json:"model"`
}
