package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// --- Anchors: three fields ---
// slug:        URL path (/w/physics)
// description: human-readable, for the /about page
// centerpoint: ~512 chars optimized for embedding quality — what should match

type Anchor struct {
	Slug        string
	Label       string
	Description string // human-readable
	Centerpoint string // embedding-optimized (~512 chars)
}

var anchors = []Anchor{
	{
		Slug: "physics", Label: "Physics",
		Description: "Fundamental physics — from quantum mechanics to astrophysics. Theoretical and experimental.",
		Centerpoint: "Quantum mechanics wave functions Schrodinger equation particle physics Standard Model quarks leptons bosons general relativity spacetime curvature gravitational waves condensed matter superconductivity topological phases thermodynamics entropy statistical mechanics Boltzmann distribution astrophysics black holes neutron stars cosmology dark matter dark energy CMB inflation string theory quantum field theory Feynman diagrams renormalization experimental physics CERN LHC detector design spectroscopy laser physics optics photonics nuclear physics fusion fission plasma physics",
	},
	{
		Slug: "compilers", Label: "Compilers & Languages",
		Description: "Compiler design, language implementation, parsing, type systems, code generation.",
		Centerpoint: "Compiler design parsing lexing tokenizer abstract syntax tree AST type checking type inference Hindley-Milner type system code generation LLVM IR intermediate representation SSA static single assignment register allocation instruction selection optimization passes dead code elimination constant folding loop unrolling inlining JIT just-in-time compilation interpreters bytecode virtual machine garbage collection memory management language design syntax semantics grammars context-free grammar PEG recursive descent parser generator yacc bison self-hosting bootstrapping",
	},
	{
		Slug: "distsys", Label: "Distributed Systems",
		Description: "Consensus, replication, fault tolerance, and everything that breaks at scale.",
		Centerpoint: "Distributed systems consensus protocol Raft Paxos Byzantine fault tolerance replication leader election log-structured merge tree LSM CAP theorem consistency availability partition tolerance CRDT conflict-free replicated data types eventual consistency linearizability serializability distributed database sharding partitioning consistent hashing gossip protocol failure detection heartbeat vector clocks Lamport timestamps causal ordering message queue event sourcing CQRS microservices service mesh load balancing circuit breaker observability distributed tracing",
	},
	{
		Slug: "ml", Label: "Machine Learning",
		Description: "Neural networks, LLMs, training, embeddings — technical depth over hype.",
		Centerpoint: "Machine learning neural network transformer architecture attention mechanism self-attention multi-head attention large language model LLM GPT BERT training fine-tuning RLHF reinforcement learning from human feedback gradient descent backpropagation loss function cross-entropy softmax embedding vector representation dimensionality reduction PCA t-SNE cosine similarity semantic search retrieval augmented generation RAG vector database tokenization BPE sentencepiece convolutional neural network recurrent LSTM computer vision object detection diffusion model inference optimization quantization",
	},
	{
		Slug: "security", Label: "Security",
		Description: "Cryptography, vulnerability research, reverse engineering, threat modeling.",
		Centerpoint: "Information security cryptography encryption AES RSA elliptic curve Diffie-Hellman TLS certificate X.509 PKI hash function SHA-256 digital signature vulnerability CVE exploit buffer overflow use-after-free RCE remote code execution reverse engineering binary analysis disassembly decompiler malware analysis threat modeling OWASP SQL injection XSS CSRF authentication authorization OAuth JWT zero trust supply chain security dependency confusion code signing sandboxing privilege escalation penetration testing CTF",
	},
	{
		Slug: "systems", Label: "Systems Programming",
		Description: "OS internals, kernels, io_uring, drivers, performance — close to the metal.",
		Centerpoint: "Operating system kernel syscall system call io_uring epoll kqueue async I/O event loop memory management virtual memory page table TLB cache hierarchy L1 L2 L3 NUMA memory allocator malloc mmap brk process thread scheduling context switch interrupt handler device driver block device filesystem ext4 btrfs ZFS NFS inode dentry VFS network stack TCP UDP socket eBPF tracing perf profiling flamegraph CPU pipeline branch prediction SIMD vectorization assembly x86 ARM Rust C systems programming unsafe pointer",
	},
	{
		Slug: "golang", Label: "Go",
		Description: "The Go programming language — idioms, concurrency, standard library, internals.",
		Centerpoint: "Go programming language golang goroutine channel select context cancellation sync mutex WaitGroup errgroup interface embedding struct method receiver pointer value generics type parameter constraint standard library net/http io Reader Writer json encoding testing benchmark table-driven test go mod module dependency go build compile link garbage collector GC runtime scheduler M P G model defer panic recover error handling wrapping sentinel slice map string rune package design API Go proverbs concurrency patterns fan-out fan-in pipeline",
	},
	{
		Slug: "webdev", Label: "Web Development",
		Description: "Frontend, backend, CSS, JavaScript, frameworks, browser APIs.",
		Centerpoint: "Web development frontend React Vue Svelte Angular component state management hooks context reducer CSS flexbox grid animation responsive design JavaScript TypeScript async await promise fetch API DOM manipulation browser rendering layout paint composite WebSocket server-sent events service worker progressive web app PWA HTML semantic accessibility ARIA screen reader REST GraphQL API endpoint middleware routing server-side rendering static site generation Next.js Remix Astro bundler Vite webpack module ESM npm package",
	},
	{
		Slug: "devtools", Label: "Developer Tools",
		Description: "Editors, terminals, CI/CD, containers, build systems, productivity.",
		Centerpoint: "Developer tools IDE editor Vim Neovim VS Code terminal shell bash zsh fish dotfiles configuration git version control branch merge rebase cherry-pick CI continuous integration CD deployment pipeline GitHub Actions Jenkins Docker container image Dockerfile Kubernetes pod service deployment orchestration development environment devcontainer debugger breakpoint profiler linter formatter code review pull request build system Make CMake Bazel Nix package manager homebrew apt npm cargo",
	},
	{
		Slug: "philosophy", Label: "Philosophy",
		Description: "Philosophy of mind, epistemology, ethics, consciousness — serious thought.",
		Centerpoint: "Philosophy of mind consciousness hard problem qualia phenomenology subjective experience intentionality epistemology knowledge justified true belief skepticism rationalism empiricism metaphysics ontology free will determinism compatibilism ethics moral realism utilitarianism deontology virtue ethics political philosophy justice Rawls libertarianism communitarianism existentialism Sartre Heidegger Dasein phenomenology Husserl Buddhist philosophy Abhidhamma dependent origination emptiness sunyata pragmatism Wittgenstein language game philosophy of science falsifiability Kuhn paradigm",
	},
	{
		Slug: "finance", Label: "Finance & Trading",
		Description: "Quantitative finance, options, portfolio management, market microstructure.",
		Centerpoint: "Quantitative finance options trading Black-Scholes Greeks delta gamma theta vega implied volatility options chain put call spread straddle strangle iron condor wheel strategy covered call cash-secured put portfolio management modern portfolio theory Sharpe ratio alpha beta risk-adjusted return Monte Carlo simulation backtesting algorithmic trading market microstructure order book bid ask spread limit order market order VWAP TWAP high-frequency trading exchange matching engine derivatives pricing yield curve fixed income",
	},
	{
		Slug: "biology", Label: "Biology & Biotech",
		Description: "Molecular biology, genetics, synthetic biology, neuroscience.",
		Centerpoint: "Biology molecular biology DNA RNA protein gene expression transcription translation ribosome genetics genomics CRISPR Cas9 gene editing synthetic biology BioBricks genetic circuit metabolic engineering bioinformatics sequence alignment BLAST phylogenetics structural biology protein folding AlphaFold drug discovery high-throughput screening neuroscience neuron synapse neurotransmitter brain imaging fMRI electrophysiology computational neuroscience cell biology microscopy flow cytometry PCR sequencing next-generation evolution natural selection",
	},
	{
		Slug: "math", Label: "Mathematics",
		Description: "Pure and applied math — algebra, topology, number theory, proofs.",
		Centerpoint: "Mathematics pure applied algebra group theory ring field vector space linear algebra eigenvalue matrix decomposition topology manifold homology cohomology fundamental group number theory prime Riemann zeta function modular arithmetic analysis real complex measure Lebesgue integration functional Hilbert Banach space combinatorics graph theory probability measure theory stochastic process Markov chain random variable distribution category theory functor natural transformation monad mathematical logic proof Godel incompleteness set theory axiom",
	},
	{
		Slug: "startups", Label: "Startups & Business",
		Description: "Entrepreneurship, product, growth, distribution, bootstrapping.",
		Centerpoint: "Startup entrepreneurship founder product market fit MVP minimum viable product customer discovery lean startup iteration pivot business model revenue pricing monetization growth distribution channel acquisition retention churn funnel conversion rate SEO content marketing launch Product Hunt Hacker News fundraising venture capital angel investor seed Series A bootstrapping indie hacker solo founder SaaS ARR MRR unit economics LTV CAC burn rate runway market sizing go-to-market strategy competitor analysis",
	},
	{
		Slug: "writing", Label: "Writing",
		Description: "Technical writing, blogging, essays, craft, documentation.",
		Centerpoint: "Writing technical blog essay documentation copywriting editing prose style voice tone clarity concision structure outline draft revision publish newsletter Substack personal knowledge management Zettelkasten note-taking Obsidian Roam Logseq markdown content strategy narrative nonfiction essay personal argument thesis evidence rhetoric persuasion audience readability plain language API documentation tutorial guide how-to reference architecture decision record ADR changelog README",
	},
	{
		Slug: "databases", Label: "Databases",
		Description: "SQLite, PostgreSQL, query optimization, storage engines, internals.",
		Centerpoint: "Database SQLite PostgreSQL MySQL query optimization execution plan index B-tree B+tree LSM log-structured merge hash index covering index composite key SQL JOIN aggregate window function CTE recursive common table expression transaction ACID isolation level serializable snapshot MVCC multi-version concurrency control WAL write-ahead log checkpoint vacuum replication streaming logical sharding connection pooling prepared statement ORM migration schema design normalization denormalization time-series InfluxDB graph Neo4j embedded storage engine RocksDB LevelDB",
	},
	{
		Slug: "climate", Label: "Climate & Energy",
		Description: "Climate science, renewable energy, grid, nuclear, electrification.",
		Centerpoint: "Climate change global warming greenhouse gas carbon dioxide methane emission reduction Paris Agreement IPCC climate model temperature anomaly sea level rise renewable energy solar photovoltaic wind turbine offshore onshore nuclear fission fusion power plant grid infrastructure transmission distribution battery storage lithium-ion sodium-ion energy density capacity factor intermittency baseload peak demand response electrification heat pump EV electric vehicle charging carbon capture sequestration hydrogen fuel cell geothermal energy policy carbon tax cap trade",
	},
	{
		Slug: "agents", Label: "AI Agents",
		Description: "Autonomous AI agents — tool use, multi-agent, memory, planning.",
		Centerpoint: "AI agent autonomous agent tool use function calling LLM orchestration multi-agent system agent communication protocol memory persistence context window planning reasoning chain-of-thought ReAct agent framework LangChain AutoGPT CrewAI agent loop observation action reflection SOUL.md agent identity prompt engineering system prompt agent-to-agent collaboration task decomposition agentic coding assistant Claude Anthropic OpenAI GPT agent platform Moltbook MoltX agent social network skills commands MCP model context protocol",
	},
	{
		Slug: "crypto", Label: "Crypto & Blockchain",
		Description: "Blockchain, tokens, DeFi, smart contracts — technical, not shilling.",
		Centerpoint: "Blockchain cryptocurrency Bitcoin Ethereum smart contract Solidity EVM token ERC-20 ERC-721 NFT DeFi decentralized finance AMM automated market maker liquidity pool yield farming staking proof of work proof of stake consensus mechanism mining validator node wallet private key public key address transaction hash block merkle tree gas fee Layer 2 rollup ZK zero-knowledge proof bridge cross-chain DEX decentralized exchange governance DAO token mint airdrop",
	},
	{
		Slug: "gaming", Label: "Gaming",
		Description: "Game development, engines, modding, game design, esports.",
		Centerpoint: "Game development game engine Unity Unreal Godot rendering pipeline shader HLSL GLSL graphics OpenGL Vulkan DirectX 3D modeling animation rigging physics engine collision detection rigid body procedural generation terrain noise Perlin simplex game design mechanics level design narrative branching dialogue system inventory crafting multiplayer netcode lag compensation client prediction ECS entity component system pixel art sprite tilemap game jam modding Skyrim Bethesda Creation Kit plugin mod manager esports competitive gaming",
	},
}

// --- OpenAI Embedding API ---

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func embed(texts []string, dims int) ([][]float64, int, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, 0, fmt.Errorf("OPENAI_API_KEY not set")
	}

	req := embeddingRequest{
		Model:      "text-embedding-3-small",
		Input:      texts,
		Dimensions: dims,
	}
	body, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("openai %d: %s", resp.StatusCode, string(b))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}

	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	vecs := make([][]float64, len(result.Data))
	for i, d := range result.Data {
		vecs[i] = d.Embedding
	}
	return vecs, result.Usage.TotalTokens, nil
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// --- Post from JSONL ---

type Post struct {
	Title   string `json:"title"`
	Text    string `json:"text"`
	Submolt string `json:"submolt"`
	Author  string `json:"author"`
	Score   int    `json:"score"`
	Source  string // set after load
}

func loadMoltPosts(path string) ([]Post, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var posts []Post
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var p Post
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue
		}
		p.Source = "molt"
		posts = append(posts, p)
	}
	return posts, nil
}

func main() {
	fmt.Println("=== WingThing Social — Embedding Experiment v2 ===")
	fmt.Println("=== Three-field anchors + Moltbook slop filter  ===")
	fmt.Println()

	// 1. Embed anchor centerpoints
	fmt.Println("[1/4] Embedding 20 anchor centerpoints at 512 dims...")
	centerpointTexts := make([]string, len(anchors))
	for i, a := range anchors {
		centerpointTexts[i] = a.Centerpoint
	}
	anchorVecs, tokens, err := embed(centerpointTexts, 512)
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed anchors: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   %d anchors embedded (%d tokens)\n\n", len(anchorVecs), tokens)

	// 2. Load posts
	fmt.Println("[2/4] Loading posts...")
	moltPath := "/tmp/molt_posts.jsonl"
	posts, err := loadMoltPosts(moltPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load molt posts: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Moltbook: %d posts\n", len(posts))
	fmt.Printf("   Total: %d posts\n\n", len(posts))

	// 3. Embed posts (use text field which has title + content)
	fmt.Println("[3/4] Embedding posts...")
	postTexts := make([]string, len(posts))
	for i, p := range posts {
		postTexts[i] = p.Text
	}

	var postVecs [][]float64
	totalTokens := 0
	for i := 0; i < len(postTexts); i += 50 {
		end := i + 50
		if end > len(postTexts) {
			end = len(postTexts)
		}
		vecs, tok, err := embed(postTexts[i:end], 512)
		if err != nil {
			fmt.Fprintf(os.Stderr, "embed posts batch %d: %v\n", i/50, err)
			os.Exit(1)
		}
		postVecs = append(postVecs, vecs...)
		totalTokens += tok
	}
	fmt.Printf("   %d posts embedded (%d tokens)\n\n", len(postVecs), totalTokens)

	// 4. Assignment
	fmt.Println("[4/4] Computing assignments...")
	fmt.Println()

	type assignment struct {
		slug       string
		similarity float64
	}

	spamThreshold := 0.25
	assignThreshold := 0.40 // top-2 above this get assigned

	stats := make(map[string]int)
	for _, a := range anchors {
		stats[a.Slug] = 0
	}

	var swallowed []int
	var frontier []int

	fmt.Println("────────────────────────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("%-50s  %-14s %-5s  %-14s %-5s  %-8s\n", "POST", "ANCHOR #1", "SIM", "ANCHOR #2", "SIM", "STATUS")
	fmt.Println("────────────────────────────────────────────────────────────────────────────────────────────────")

	for i, pv := range postVecs {
		var assignments []assignment
		for j, av := range anchorVecs {
			sim := cosine(pv, av)
			assignments = append(assignments, assignment{anchors[j].Slug, sim})
		}
		sort.Slice(assignments, func(a, b int) bool {
			return assignments[a].similarity > assignments[b].similarity
		})

		best := assignments[0]
		second := assignments[1]

		title := posts[i].Title
		if len(title) > 47 {
			title = title[:44] + "..."
		}

		status := ""
		if best.similarity < spamThreshold {
			swallowed = append(swallowed, i)
			status = "SWALLOW"
		} else if best.similarity < assignThreshold {
			frontier = append(frontier, i)
			status = "FRONTIER"
		} else {
			// Assign to top-2 above threshold
			stats[best.slug]++
			if second.similarity >= assignThreshold {
				stats[second.slug]++
				status = fmt.Sprintf("-> 2 anchors")
			} else {
				status = fmt.Sprintf("-> 1 anchor")
			}
		}

		fmt.Printf("%-50s  /w/%-11s %5.3f  /w/%-11s %5.3f  %s\n",
			title, best.slug, best.similarity, second.slug, second.similarity, status)
	}

	// Summary
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════════════════════")
	fmt.Println("ANCHOR DISTRIBUTION (posts assigned, sim >= 0.40)")
	fmt.Println("════════════════════════════════════════════════════════════════════════════════")

	type anchorStat struct {
		slug  string
		count int
	}
	var statList []anchorStat
	for slug, count := range stats {
		statList = append(statList, anchorStat{slug, count})
	}
	sort.Slice(statList, func(i, j int) bool {
		return statList[i].count > statList[j].count
	})

	for _, s := range statList {
		bar := strings.Repeat("█", s.count)
		fmt.Printf("/w/%-14s %3d  %s\n", s.slug, s.count, bar)
	}

	fmt.Println()
	assigned := len(posts) - len(frontier) - len(swallowed)
	fmt.Printf("Total posts:    %d\n", len(posts))
	fmt.Printf("Assigned:       %d (sim >= %.2f)\n", assigned, assignThreshold)
	fmt.Printf("Frontier:       %d (sim %.2f-%.2f)\n", len(frontier), spamThreshold, assignThreshold)
	fmt.Printf("Swallowed:      %d (sim < %.2f)\n", len(swallowed), spamThreshold)

	// Anchor-anchor similarity
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════════════════════")
	fmt.Println("ANCHOR-ANCHOR SIMILARITY (top 10 closest — boundary check)")
	fmt.Println("════════════════════════════════════════════════════════════════════════════════")

	type pair struct {
		a, b string
		sim  float64
	}
	var pairs []pair
	for i := 0; i < len(anchorVecs); i++ {
		for j := i + 1; j < len(anchorVecs); j++ {
			sim := cosine(anchorVecs[i], anchorVecs[j])
			pairs = append(pairs, pair{anchors[i].Slug, anchors[j].Slug, sim})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].sim > pairs[j].sim
	})

	for k := 0; k < 10 && k < len(pairs); k++ {
		p := pairs[k]
		fmt.Printf("/w/%-14s <-> /w/%-14s  %5.3f\n", p.a, p.b, p.sim)
	}

	// Spam/swallowed detail
	if len(swallowed) > 0 {
		fmt.Println()
		fmt.Println("════════════════════════════════════════════════════════════════════════════════")
		fmt.Println("SWALLOWED (spam/noise)")
		fmt.Println("════════════════════════════════════════════════════════════════════════════════")
		for _, idx := range swallowed {
			title := posts[idx].Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			bestSim := 0.0
			bestSlug := ""
			for j, av := range anchorVecs {
				sim := cosine(postVecs[idx], av)
				if sim > bestSim {
					bestSim = sim
					bestSlug = anchors[j].Slug
				}
			}
			fmt.Printf("  %-60s  nearest: /w/%-10s %5.3f\n", title, bestSlug, bestSim)
		}
	}

	fmt.Println()
	fmt.Printf("OpenAI tokens used: %d (cost: ~$%.4f)\n", tokens+totalTokens, float64(tokens+totalTokens)*0.00002/1000.0*1000)
}
