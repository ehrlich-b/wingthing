# Egg Config Inheritance

## Problem

Default egg.yaml blocks `~/.ssh`, port 22, and other sensitive paths. This is correct. But the only way to poke a hole today is to write a full replacement config that redeclares every deny rule, every mount, everything. You can't say "defaults + SSH."

Agent profiles already solve this for agents — claude's profile adds API domains, env vars, and write dirs without touching the base config. Projects need the same mechanism.

## Design

### The stack

Every egg config implicitly inherits from a base. The resolution order:

```
built-in default          ← always the root, published and documented
  └─ ~/.wingthing/egg.yaml  ← user global (optional)
       └─ ./egg.yaml          ← project (optional)
            └─ agent profile     ← auto-merged last (unchanged from today)
```

Each layer is **additive** to its parent. You only declare what you're changing.

### The `base` key

Controls what a config inherits from.

| Value | Meaning |
|-------|---------|
| *(omitted)* | Inherit from the next layer up (implicit, the common case) |
| `none` | Empty slate. No defaults, no denies, no mounts. You define everything. |
| `default` | Explicit reference to the built-in default (same as omitting) |
| `<name>` | Named base from `~/.wingthing/bases/<name>.yaml` |
| `<path>` | Relative path to another egg.yaml file |
| `{name:, fs:, network:, env:}` | Object form with per-section masks (see below) |

Named bases can themselves declare `base:`, forming a chain. Circular references are an error (detect at load time, max depth 10).

### Per-section masks (object form)

The `base` field can be an object to control inheritance per-section. Each section (fs, env, network) independently controls its inheritance source.

```yaml
# Object form — per-section masks
base:
  name: strict          # parent for unmasked sections (optional, default = built-in)
  fs: none              # cut fs inheritance — parent's fs becomes empty
  env: prod-env         # source env from prod-env.yaml's resolved chain
  network: none         # cut network inheritance
```

Section mask values:
- **omitted** — inherit from parent (fall through parent's chain)
- **`none`** — cut inheritance; parent's value for this section becomes nil
- **`<name-or-path>`** — resolve that file's full base chain, extract the section

When you reference a file for a section mask, you get the **resolved** result (after that file's own base chain resolves), not the raw YAML.

#### Resolution example

```yaml
# child.yaml
base:
  env: team-env

# team-env.yaml
base: corp-defaults
env: [TEAM_KEY]

# corp-defaults.yaml (base: none)
env: [HOME, PATH]
```

Resolving `child.yaml`:
1. Resolve parent (default, since no `base.name`) — built-in defaults (fs, env, network)
2. Apply section masks:
   - `env` mask = `team-env` → resolve team-env.yaml → resolves corp-defaults → gets `[HOME, PATH]` → merges with team-env → `[HOME, PATH, TEAM_KEY]`
   - fs/network: no mask → keep parent (built-in defaults)
3. Merge masked parent + child's own overrides

#### Cycle prevention

A YAML file may appear at most once in any resolution, across all branches (base chain + section mask refs). The visited map is shared across the entire resolution tree. If a section mask references a file already in the base chain, it's an error.

Masks are invalid with `base: none` (nothing to mask).

#### Interaction with agent auto-drill

Agent profiles are applied AFTER config resolution. Per-section masks don't affect auto-drilling — agents always get their declared network, env, and fs holes regardless of what the egg config specifies.

### Merge rules

| Field | Merge behavior |
|-------|---------------|
| `fs` | Appended. Explicit `ro:` or `rw:` for a path **overrides** a `deny:` of the same path from a parent. |
| `network` | Unioned. Child domains added to parent domains. `"*"` in any layer = full network. |
| `env` | Unioned. Child vars added to parent vars. `"*"` in any layer = all env. |
| `resources` | Scalar override — child value wins per-field (cpu, memory, max_fds). |
| `shell` | Scalar override — child wins. |
| `dangerously_skip_permissions` | OR — `true` in any layer means `true`. |

### The published default

The built-in default is the root of all inheritance. It's what you get with zero config. Published here so users know what they're extending:

```yaml
# built-in default (implicit root base)
fs:
  - "rw:./"
  - "deny:~/.ssh"
  - "ro:~/.ssh/known_hosts"       # preserved so SSH doesn't prompt
  - "deny:~/.gnupg"
  - "deny:~/.aws"
  - "deny:~/.docker"
  - "deny:~/.kube"
  - "deny:~/.netrc"
  - "deny:~/.bash_history"
  - "deny:~/.zsh_history"
# network: none
# env: (essentials only — HOME, PATH, TERM, LANG, USER)
# resources: (none — no limits)
# shell: $SHELL
# dangerously_skip_permissions: true
```

## Examples

### Poke a hole for SSH

```yaml
# ./egg.yaml — 4 lines, inherits everything else from default
fs:
  - "ro:~/.ssh"      # overrides deny:~/.ssh from default
network:
  - "*:22"            # add port 22
env:
  - SSH_AUTH_SOCK     # pass SSH agent socket
```

Result: CWD writable, ~/.gnupg denied, ~/.aws denied, ... (all defaults preserved), plus ~/.ssh readable, port 22 open, SSH_AUTH_SOCK passed. Agent profile (claude API, etc.) merged on top.

### Team lockdown base

```yaml
# ~/.wingthing/bases/strict.yaml
base: none
fs:
  - "rw:./"
  - "deny:~/.ssh"
  - "deny:~/.gnupg"
  - "deny:~/.aws"
  - "deny:~/.docker"
  - "deny:~/.kube"
  - "deny:~/.netrc"
  - "deny:~/.bash_history"
  - "deny:~/.zsh_history"
resources:
  cpu: "120s"
  memory: "2GB"
  max_fds: 512
```

```yaml
# ./egg.yaml — project extends strict, adds what it needs
base: strict
fs:
  - "rw:~/data"
network:
  - "api.internal.corp:443"
env:
  - CORP_API_KEY
```

### Full access (development)

```yaml
# ./egg.yaml
base: none
fs: []                # empty = no restrictions
network: "*"
env: "*"
```

### Project-local base

```yaml
# ./bases/ci.yaml
base: default
resources:
  cpu: "60s"
  memory: "1GB"
```

```yaml
# ./egg.yaml
base: ./bases/ci.yaml
env:
  - CI_TOKEN
```

## Implementation

### Config struct changes

```go
type EggConfig struct {
    Base                       string       `yaml:"base"`  // NEW
    FS                         []string     `yaml:"fs"`
    Network                    NetworkField `yaml:"network"`
    Env                        EnvField     `yaml:"env"`
    Resources                  EggResources `yaml:"resources"`
    Shell                      string       `yaml:"shell"`
    DangerouslySkipPermissions bool         `yaml:"dangerously_skip_permissions"`
}
```

### New functions

```go
// ResolveEggConfig loads an egg.yaml and recursively resolves its base chain,
// returning a fully merged config. Returns error on circular refs or depth > 10.
func ResolveEggConfig(path string) (*EggConfig, error)

// MergeEggConfig merges child on top of parent using the rules above.
func MergeEggConfig(parent, child *EggConfig) *EggConfig

// ResolveBase resolves a base reference to a file path.
// "none" → nil, "default" → nil (use DefaultEggConfig), "<name>" → ~/.wingthing/bases/<name>.yaml,
// "<path>" → resolved relative to the child config's directory.
func ResolveBase(base string, configDir string) (string, error)
```

### DiscoverEggConfig changes

Today: first-match (project OR user OR default).
After: resolve full chain.

```go
func DiscoverEggConfig(cwd string, wingDefault *EggConfig) *EggConfig {
    // 1. Start with built-in default
    result := DefaultEggConfig()

    // 2. If ~/.wingthing/egg.yaml exists, resolve its chain and merge
    if userCfg := loadAndResolve("~/.wingthing/egg.yaml"); userCfg != nil {
        result = MergeEggConfig(result, userCfg)
    }

    // 3. If ./egg.yaml exists, resolve its chain and merge
    if projCfg := loadAndResolve(cwd + "/egg.yaml"); projCfg != nil {
        result = MergeEggConfig(result, projCfg)
    }

    // 4. Agent profile merged later in RunSession() (unchanged)
    return result
}
```

### fs override semantics

When merging fs lists, an explicit `ro:` or `rw:` for path P overrides a `deny:` for path P from a parent:

```go
func mergeFS(parent, child []string) []string {
    // Collect child allow paths (ro: or rw:)
    childAllows := set of paths from child where mode is ro or rw

    // Filter parent: drop deny entries whose path is in childAllows
    filtered := parent entries where !(mode == "deny" && path in childAllows)

    // Append child entries
    return append(filtered, child...)
}
```

### Network port granularity

Current `NetworkNeed` is a coarse enum (None/Local/HTTPS/Full). The `*:22` syntax needs per-port support.

New network entry format: `domain`, `domain:port`, `*:port`, `*`, `none`, `localhost`.

```go
type NetworkNeed struct {
    Level NetworkLevel  // None, Local, Full (keep for quick checks)
    Ports []uint16      // Specific ports to allow (in addition to level)
}
```

macOS seatbelt already filters by port (443/80 for HTTPS). Adding 22 is one more `(allow network-outbound (remote tcp "*:22"))` rule. Linux needs iptables in the namespace or an extension to the current approach.

## Migration

No breaking changes. Existing egg.yaml files with no `base` key behave identically — they implicitly extend the default, and since the current first-match behavior means they already "replace" the default, the only difference is that defaults are now preserved underneath. If someone wrote a full config expecting it to be the complete picture, they add `base: none` to get the old behavior.

To be safe: log a deprecation notice if an egg.yaml has fields that look like a complete config (has its own deny rules) without `base: none`, suggesting they may want `base: none` for full control.
