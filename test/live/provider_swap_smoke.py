#!/usr/bin/env python3
"""Live provider-swap smoke matrix for Wingthing releases.

This is intentionally opt-in. It exercises real upstream harnesses and local
models, and it treats an exact filesystem artifact as the success signal.
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Callable


EXPECTED = b"Hello World!"
PROVIDER_SUBSTITUTION_HARNESSES = {
    "claude", "codex", "gemini", "hermes", "ollama", "opencode"
}
ROOT = Path(__file__).resolve().parents[2]
CONFIG_DIR = Path(__file__).resolve().parent


@dataclass
class Result:
    name: str
    ok: bool
    seconds: float
    detail: str


def env_value(name: str, default: str) -> str:
    value = os.environ.get(name, "").strip()
    return value or default


def resolve_binary(env_name: str, command: str) -> Path:
    configured = os.environ.get(env_name, "").strip()
    candidate = configured or shutil.which(command)
    if not candidate:
        raise RuntimeError(f"{env_name} is unset and {command!r} is not on PATH")
    path = Path(candidate).expanduser().resolve()
    if not path.is_file() or not os.access(path, os.X_OK):
        raise RuntimeError(f"{env_name} does not resolve to an executable: {path}")
    return path


def request_json(url: str, payload: dict | None = None, timeout: float = 10) -> dict:
    data = None if payload is None else json.dumps(payload).encode()
    headers = {"Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.load(response)


def install_template(name: str, destination: Path, replacements: dict[str, str]) -> None:
    body = (CONFIG_DIR / name).read_text()
    for old, new in replacements.items():
        body = body.replace(old, new)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(body)


def assert_artifact(path: Path) -> None:
    if not path.is_file():
        raise RuntimeError(f"missing artifact {path}")
    actual = path.read_bytes()
    if actual != EXPECTED:
        raise RuntimeError(f"artifact {path} contains {actual!r}, want {EXPECTED!r}")


def run_process(
    name: str,
    command: list[str],
    cwd: Path,
    env: dict[str, str],
    logs: Path,
    timeout: float,
    artifact: Path | None = None,
    marker: str | None = None,
) -> Result:
    started = time.monotonic()
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            output, _ = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                output, _ = process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                output, _ = process.communicate()
            output = output or ""
            (logs / f"{name.replace('/', '-')}.log").write_text(output)
            raise RuntimeError(f"timed out after {timeout:.0f}s; tail: {output[-1200:].strip()}")
        output = output or ""
        (logs / f"{name.replace('/', '-')}.log").write_text(output)
        if process.returncode != 0:
            raise RuntimeError(f"exit {process.returncode}: {output[-1200:].strip()}")
        if artifact is not None:
            assert_artifact(artifact)
        if marker is not None and marker not in output:
            raise RuntimeError(f"missing marker {marker!r}; tail: {output[-1200:].strip()}")
        return Result(name, True, time.monotonic() - started, "artifact verified" if artifact else "marker verified")
    except Exception as error:
        return Result(name, False, time.monotonic() - started, str(error))


def run_ollama_tool_api(
    name: str,
    ollama_url: str,
    model: str,
    artifact: Path,
    logs: Path,
    timeout: float,
) -> Result:
    started = time.monotonic()
    payload = {
        "model": model,
        "stream": False,
        "options": {"temperature": 0, "num_ctx": 32768},
        "messages": [
            {
                "role": "user",
                "content": (
                    "Call write_file exactly once with path "
                    f"{artifact} and content Hello World!. Return no prose."
                ),
            }
        ],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "write_file",
                    "description": "Write exact text to a file",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "path": {"type": "string"},
                            "content": {"type": "string"},
                        },
                        "required": ["path", "content"],
                    },
                },
            }
        ],
    }
    try:
        response = request_json(f"{ollama_url}/api/chat", payload, timeout=timeout)
        (logs / f"{name.replace('/', '-')}.json").write_text(json.dumps(response, indent=2))
        calls = response.get("message", {}).get("tool_calls", [])
        if len(calls) != 1:
            raise RuntimeError(f"expected one tool call, got {len(calls)}")
        function = calls[0].get("function", {})
        if function.get("name") != "write_file":
            raise RuntimeError(f"unexpected tool name {function.get('name')!r}")
        arguments = function.get("arguments", {})
        if isinstance(arguments, str):
            arguments = json.loads(arguments)
        if arguments != {"path": str(artifact), "content": "Hello World!"}:
            raise RuntimeError(f"unexpected tool arguments {arguments!r}")
        artifact.parent.mkdir(parents=True, exist_ok=True)
        artifact.write_bytes(EXPECTED)
        assert_artifact(artifact)
        return Result(name, True, time.monotonic() - started, "structured call dispatched")
    except Exception as error:
        return Result(name, False, time.monotonic() - started, str(error))


def run_mcp_meta_layer(
    wt: Path,
    cwd: Path,
    env: dict[str, str],
    logs: Path,
    timeout: float,
) -> list[Result]:
    started = time.monotonic()
    requests = [
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-11-25",
                "capabilities": {},
                "clientInfo": {"name": "wingthing-release-smoke", "version": "1"},
            },
        },
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {
                "name": "prompt_run",
                "arguments": {
                    "prompt": "Reply exactly MCP_RAW_OK and nothing else.",
                    "agent": "ollama",
                    "cwd": str(cwd),
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 4,
            "method": "tools/call",
            "params": {
                "name": "prompt_save",
                "arguments": {
                    "name": "release-hello",
                    "description": "provider swap release canary",
                    "template": "Reply exactly {{.marker}} and nothing else.",
                    "variables": ["marker"],
                    "agent": "ollama",
                    "cwd": str(cwd),
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 5,
            "method": "tools/call",
            "params": {
                "name": "prompt_run",
                "arguments": {
                    "prompt_name": "release-hello",
                    "variables": {"marker": "MCP_ASSET_OK"},
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 6,
            "method": "tools/call",
            "params": {
                "name": "prompt_loop",
                "arguments": {
                    "prompt": "Reply exactly MCP_LOOP_OK and nothing else.",
                    "agent": "ollama",
                    "cwd": str(cwd),
                    "max_iterations": 2,
                    "until_contains": "MCP_LOOP_OK",
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 7,
            "method": "tools/call",
            "params": {
                "name": "swarm_run",
                "arguments": {
                    "name": "release hello swarm",
                    "cwd": str(cwd),
                    "max_parallel": 1,
                    "nodes": [
                        {
                            "id": "source",
                            "prompt": "Reply exactly MCP_SOURCE_OK and nothing else.",
                            "agent": "ollama",
                        },
                        {
                            "id": "finish",
                            "prompt": "Ignore dependency prose and reply exactly MCP_SWARM_OK and nothing else.",
                            "agent": "ollama",
                            "depends_on": ["source"],
                        },
                    ],
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 8,
            "method": "tools/call",
            "params": {"name": "wingthing_capabilities", "arguments": {}},
        },
    ]
    input_text = "".join(json.dumps(request) + "\n" for request in requests)
    try:
        completed = subprocess.run(
            [str(wt), "mcp", "stdio"],
            cwd=cwd,
            env=env,
            input=input_text,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout * 6,
            check=False,
        )
        (logs / "mcp-meta.stdout.log").write_text(completed.stdout or "")
        (logs / "mcp-meta.stderr.log").write_text(completed.stderr or "")
        if completed.returncode != 0:
            raise RuntimeError(f"MCP server exit {completed.returncode}: {(completed.stderr or '')[-1200:]}")
        responses = {}
        for line in (completed.stdout or "").splitlines():
            response = json.loads(line)
            if "id" in response:
                responses[response["id"]] = response

        elapsed = time.monotonic() - started

        def tool_data(response_id: int) -> dict:
            response = responses[response_id]
            if "error" in response:
                raise RuntimeError(f"MCP {response_id} protocol error: {response['error']}")
            result = response["result"]
            if result.get("isError"):
                raise RuntimeError(f"MCP {response_id} tool error: {result.get('structuredContent')}")
            return result["structuredContent"]

        checks: list[tuple[str, Callable[[], None]]] = []

        def discovery_check() -> None:
            tools = responses[2]["result"]["tools"]
            names = {tool["name"] for tool in tools}
            required = {"prompt_save", "prompt_run", "prompt_loop", "swarm_run"}
            if len(tools) != 14 or not required.issubset(names):
                raise RuntimeError(f"unexpected MCP tools: count={len(tools)} names={sorted(names)}")
            agents = tool_data(8).get("agents", [])
            substitutable = {
                agent["name"] for agent in agents if agent.get("provider_substitution")
            }
            release_canaries = {
                agent["name"] for agent in agents if agent.get("release_canary")
            }
            if substitutable != PROVIDER_SUBSTITUTION_HARNESSES:
                raise RuntimeError(
                    "provider-substitution catalog does not match live matrix: "
                    f"catalog={sorted(substitutable)} matrix={sorted(PROVIDER_SUBSTITUTION_HARNESSES)}"
                )
            if release_canaries != PROVIDER_SUBSTITUTION_HARNESSES:
                raise RuntimeError(
                    "release-canary catalog does not match live matrix: "
                    f"catalog={sorted(release_canaries)} matrix={sorted(PROVIDER_SUBSTITUTION_HARNESSES)}"
                )

        def raw_check() -> None:
            output = tool_data(3).get("output") or ""
            if "MCP_RAW_OK" not in output:
                raise RuntimeError(f"raw prompt output missing marker: {output!r}")

        def asset_check() -> None:
            saved = tool_data(4).get("prompt", {})
            if not saved.get("revision"):
                raise RuntimeError(f"saved prompt lacks immutable revision: {saved!r}")
            output = tool_data(5).get("output") or ""
            if "MCP_ASSET_OK" not in output:
                raise RuntimeError(f"saved prompt output missing marker: {output!r}")

        def loop_check() -> None:
            loop = tool_data(6)
            iterations = loop.get("iterations", [])
            if loop.get("status") != "done" or not iterations:
                raise RuntimeError(f"loop did not complete: {loop!r}")
            output = iterations[-1].get("output") or ""
            if "MCP_LOOP_OK" not in output:
                raise RuntimeError(f"loop output missing marker: {output!r}")

        def swarm_check() -> None:
            swarm = tool_data(7)
            if swarm.get("status") != "done":
                raise RuntimeError(f"swarm did not complete: {swarm!r}")
            nodes = {node["id"]: node for node in swarm.get("nodes", [])}
            output = nodes.get("finish", {}).get("task", {}).get("output") or ""
            if "MCP_SWARM_OK" not in output:
                raise RuntimeError(f"swarm reducer output missing marker: {output!r}")

        checks.extend(
            [
                ("mcp/discovery", discovery_check),
                ("mcp/prompt-raw", raw_check),
                ("mcp/prompt-asset", asset_check),
                ("mcp/loop", loop_check),
                ("mcp/swarm", swarm_check),
            ]
        )
        results = []
        for name, check in checks:
            try:
                check()
                results.append(Result(name, True, elapsed, "semantic result verified"))
            except Exception as error:
                results.append(Result(name, False, elapsed, str(error)))
        return results
    except Exception as error:
        return [Result("mcp/process", False, time.monotonic() - started, str(error))]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=("all", "direct", "wingthing"), default="all")
    parser.add_argument("--keep", action="store_true", help="retain the isolated smoke workspace")
    args = parser.parse_args()

    timeout = float(env_value("WT_SMOKE_TIMEOUT", "180"))
    ollama_url = env_value("WT_SMOKE_OLLAMA_URL", "http://127.0.0.1:11434").rstrip("/")
    litellm_url = env_value("WT_SMOKE_LITELLM_URL", "http://127.0.0.1:4000").rstrip("/")
    ollama_small = env_value("WT_SMOKE_OLLAMA_SMALL_MODEL", "qwen3:4b")
    ollama_tools = env_value("WT_SMOKE_OLLAMA_TOOL_MODEL", "qwen3:8b")
    claude_alias = env_value("WT_SMOKE_LITELLM_CLAUDE_MODEL", "wingthing-local")
    codex_alias = env_value("WT_SMOKE_LITELLM_CODEX_MODEL", "wingthing-local-8b")

    workspace = Path(tempfile.mkdtemp(prefix="wingthing-provider-swap-"))
    home = workspace / "home"
    logs = workspace / "logs"
    bin_dir = workspace / "bin"
    home.mkdir()
    logs.mkdir()
    bin_dir.mkdir()

    try:
        binaries = {
            "claude": resolve_binary("WT_SMOKE_CLAUDE_BIN", "claude"),
            "codex": resolve_binary("WT_SMOKE_CODEX_BIN", "codex"),
            "gemini": resolve_binary("WT_SMOKE_GEMINI_BIN", "gemini"),
            "hermes": resolve_binary("WT_SMOKE_HERMES_BIN", "hermes"),
            "opencode": resolve_binary("WT_SMOKE_OPENCODE_BIN", "opencode"),
            "ollama": resolve_binary("WT_SMOKE_OLLAMA_BIN", "ollama"),
        }
        wt = resolve_binary("WT_SMOKE_WT_BIN", "wt")
        for command, target in binaries.items():
            (bin_dir / command).symlink_to(target)

        codex_home = home / ".codex"
        hermes_home = home / ".hermes"
        opencode_config = home / ".config" / "opencode" / "opencode.json"
        install_template(
            "codex-config.toml",
            codex_home / "config.toml",
            {
                "http://127.0.0.1:4000": litellm_url,
                "wingthing-local-8b": codex_alias,
            },
        )
        install_template(
            "hermes-config.yaml",
            hermes_home / "config.yaml",
            {
                "http://127.0.0.1:11434": ollama_url,
                "qwen3:8b": ollama_tools,
            },
        )
        install_template("gemini-settings.json", home / ".gemini" / "settings.json", {})
        install_template(
            "opencode.json",
            opencode_config,
            {
                "http://127.0.0.1:11434": ollama_url,
                "qwen3:8b": ollama_tools,
            },
        )

        tags = request_json(f"{ollama_url}/api/tags")
        installed_models = {entry.get("name") for entry in tags.get("models", [])}
        for model in (ollama_small, ollama_tools):
            if model not in installed_models:
                raise RuntimeError(f"Ollama model {model!r} is not installed; found {sorted(installed_models)}")
        litellm_models = request_json(f"{litellm_url}/v1/models")
        litellm_ids = {entry.get("id") for entry in litellm_models.get("data", [])}
        for model in (claude_alias, codex_alias, "gemini-2.5-pro"):
            if model not in litellm_ids:
                raise RuntimeError(f"LiteLLM model {model!r} is not configured; found {sorted(litellm_ids)}")

        base_env = os.environ.copy()
        base_env.update(
            {
                "HOME": str(home),
                "PATH": str(bin_dir) + os.pathsep + base_env.get("PATH", ""),
                "NO_PROXY": "127.0.0.1,localhost",
                "no_proxy": "127.0.0.1,localhost",
                "DISABLE_AUTOUPDATER": "1",
                "DISABLE_TELEMETRY": "1",
                "CODEX_HOME": str(codex_home),
                "HERMES_HOME": str(hermes_home),
                "OPENCODE_CONFIG": str(opencode_config),
                "OPENAI_API_KEY": "sk-wingthing-local-smoke",
                "ANTHROPIC_AUTH_TOKEN": "sk-wingthing-local-smoke",
                "ANTHROPIC_BASE_URL": litellm_url,
                "ANTHROPIC_MODEL": claude_alias,
                "ANTHROPIC_DEFAULT_HAIKU_MODEL": claude_alias,
                "ANTHROPIC_DEFAULT_SONNET_MODEL": claude_alias,
                "ANTHROPIC_DEFAULT_OPUS_MODEL": claude_alias,
                "GEMINI_API_KEY": "sk-wingthing-local-smoke",
                "GOOGLE_GEMINI_BASE_URL": litellm_url,
                "GEMINI_CLI_TRUST_WORKSPACE": "true",
                "OLLAMA_HOST": ollama_url,
            }
        )

        harness_dirs = {name: workspace / name for name in binaries}
        for directory in harness_dirs.values():
            directory.mkdir()

        results: list[Result] = []

        def process_case(
            phase: str,
            name: str,
            command: list[str],
            cwd: Path,
            artifact: Path | None = None,
            marker: str | None = None,
            extra_env: dict[str, str] | None = None,
        ) -> None:
            if args.phase not in ("all", phase):
                return
            case_env = base_env.copy()
            if extra_env:
                case_env.update(extra_env)
            result = run_process(name, command, cwd, case_env, logs, timeout, artifact, marker)
            results.append(result)
            status = "PASS" if result.ok else "FAIL"
            print(f"{status:4} {name:24} {result.seconds:6.1f}s  {result.detail}", flush=True)

        claude_dir = harness_dirs["claude"]
        claude_direct = claude_dir / "hello-direct.txt"
        process_case(
            "direct",
            "claude/direct",
            [
                "claude", "--print", "--output-format", "json", "--model", claude_alias,
                "--max-turns", "4", "--dangerously-skip-permissions",
                "Use the Write tool exactly once to create hello-direct.txt in the current directory with exact content Hello World! and no trailing newline. After it succeeds reply WT_CLAUDE_DIRECT_OK.",
            ],
            claude_dir,
            claude_direct,
            "WT_CLAUDE_DIRECT_OK",
        )
        claude_wt = claude_dir / "hello-wingthing.txt"
        process_case(
            "wingthing",
            "claude/wingthing",
            [str(wt), "run", "--agent", "claude", "Use the Write tool exactly once to create hello-wingthing.txt in the current directory with exact content Hello World! and no trailing newline. After it succeeds reply WT_CLAUDE_WINGTHING_OK."],
            claude_dir,
            claude_wt,
            "WT_CLAUDE_WINGTHING_OK",
            {"WT_PROVIDER_BASE_URL": litellm_url},
        )

        codex_dir = harness_dirs["codex"]
        codex_direct = codex_dir / "hello-direct.txt"
        process_case(
            "direct",
            "codex/direct",
            [
                "codex", "exec", "--dangerously-bypass-approvals-and-sandbox",
                "--skip-git-repo-check", "--ephemeral", "--json",
                "Do not explain or show a command. Immediately call the shell tool with sandbox_permissions use_default to run: printf Hello\\ World\\! > hello-direct.txt. After it succeeds reply WT_CODEX_DIRECT_OK.",
            ],
            codex_dir,
            codex_direct,
            "WT_CODEX_DIRECT_OK",
        )
        codex_wt = codex_dir / "hello-wingthing.txt"
        process_case(
            "wingthing",
            "codex/wingthing",
            [str(wt), "run", "--agent", "codex", "Do not explain or show a command. Immediately call the shell tool with sandbox_permissions use_default to run: printf Hello\\ World\\! > hello-wingthing.txt. After it succeeds reply WT_CODEX_WINGTHING_OK."],
            codex_dir,
            codex_wt,
            "WT_CODEX_WINGTHING_OK",
            {"WT_PROVIDER_BASE_URL": f"{litellm_url}/v1"},
        )

        gemini_dir = harness_dirs["gemini"]
        gemini_direct = gemini_dir / "hello-direct.txt"
        process_case(
            "direct",
            "gemini/direct",
            [
                "gemini", "-p",
                f"Call write_file now exactly once with file_path {gemini_direct} and content Hello World!. Do not call run_shell_command and do not explain.",
                "--model", "gemini-2.5-pro", "--yolo", "--output-format", "json",
            ],
            gemini_dir,
            gemini_direct,
        )
        gemini_wt = gemini_dir / "hello-wingthing.txt"
        process_case(
            "wingthing",
            "gemini/wingthing",
            [str(wt), "run", "--agent", "gemini", f"Call write_file now exactly once with file_path {gemini_wt} and content Hello World!. Do not call run_shell_command and do not explain."],
            gemini_dir,
            gemini_wt,
            extra_env={"WT_PROVIDER_BASE_URL": litellm_url},
        )

        hermes_dir = harness_dirs["hermes"]
        hermes_direct = hermes_dir / "hello-direct.txt"
        process_case(
            "direct",
            "hermes/direct",
            ["hermes", "--yolo", "-t", "terminal", "-z", f"Call the terminal function now with command printf 'Hello World!' > {hermes_direct}. After it succeeds output WT_HERMES_DIRECT_OK."],
            hermes_dir,
            hermes_direct,
            "WT_HERMES_DIRECT_OK",
        )
        hermes_wt = hermes_dir / "hello-wingthing.txt"
        process_case(
            "wingthing",
            "hermes/wingthing",
            [str(wt), "run", "--agent", "hermes", f"Call the terminal function now with command printf 'Hello World!' > {hermes_wt}. After it succeeds output WT_HERMES_WINGTHING_OK."],
            hermes_dir,
            hermes_wt,
            "WT_HERMES_WINGTHING_OK",
            {"WT_PROVIDER_BASE_URL": f"{ollama_url}/v1", "WT_HERMES_TOOLSETS": "terminal"},
        )

        opencode_dir = harness_dirs["opencode"]
        opencode_direct = opencode_dir / "hello-direct.txt"
        process_case(
            "direct",
            "opencode/direct",
            ["opencode", "run", "--auto", "--model", f"ollama/{ollama_tools}", "--dir", str(opencode_dir), "--format", "json", f"Call the write function now with filePath {opencode_direct} and content Hello World!. Do not print JSON or explain."],
            opencode_dir,
            opencode_direct,
        )
        opencode_wt = opencode_dir / "hello-wingthing.txt"
        process_case(
            "wingthing",
            "opencode/wingthing",
            [str(wt), "run", "--agent", "opencode", f"Call the write function now with filePath {opencode_wt} and content Hello World!. Do not print JSON or explain."],
            opencode_dir,
            opencode_wt,
            extra_env={"WT_PROVIDER_BASE_URL": f"{ollama_url}/v1"},
        )

        if args.phase in ("all", "direct"):
            result = run_ollama_tool_api(
                "ollama/tool-api",
                ollama_url,
                ollama_small,
                harness_dirs["ollama"] / "hello-tool-api.txt",
                logs,
                timeout,
            )
            results.append(result)
            status = "PASS" if result.ok else "FAIL"
            print(f"{status:4} {result.name:24} {result.seconds:6.1f}s  {result.detail}", flush=True)
        process_case(
            "wingthing",
            "ollama/wingthing",
            [str(wt), "run", "--agent", "ollama", "Reply exactly WT_OLLAMA_WINGTHING_OK and nothing else."],
            harness_dirs["ollama"],
            marker="WT_OLLAMA_WINGTHING_OK",
            extra_env={"WT_PROVIDER_BASE_URL": ollama_url},
        )

        if args.phase in ("all", "wingthing"):
            mcp_env = base_env.copy()
            mcp_env["WT_PROVIDER_BASE_URL"] = ollama_url
            for result in run_mcp_meta_layer(
                wt,
                harness_dirs["ollama"],
                mcp_env,
                logs,
                timeout,
            ):
                results.append(result)
                status = "PASS" if result.ok else "FAIL"
                print(f"{status:4} {result.name:24} {result.seconds:6.1f}s  {result.detail}", flush=True)

        failures = [result for result in results if not result.ok]
        print(f"\n{len(results) - len(failures)}/{len(results)} provider-swap smoke cases passed")
        if failures:
            print(f"Evidence retained at {workspace}")
            return 1
        if args.keep:
            print(f"Evidence retained at {workspace}")
        else:
            shutil.rmtree(workspace)
        return 0
    except Exception as error:
        print(f"PRECHECK FAIL: {error}", file=sys.stderr)
        print(f"Evidence retained at {workspace}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
