"""Shared receipt vocabulary and offline provenance for the dogfood harness."""
import datetime
import hashlib
import json
from pathlib import Path
import subprocess

LOG_LIMIT = 256 * 1024
LOG_COUNT = 24
INTERVAL_SECONDS = 45 * 60
SLACK_SECONDS = 60
CHECKS = (
    "local_only_start", "local_only_daemon_boot", "terminal_start_send_stop",
    "mcp_stdio_agent_run_wait_result", "mcp_client_disconnect", "wing_restart_mid_run",
    "mcp_tasks_get_result_list_cancel", "remembered_ssh_fixture",
    "wt_claude_refuses_provider_writable_state", "wt_claude_scoped_mailbox",
    "remembered_ssh_connect", "real_codex", "cleanup",
)
OPTIONAL = {"remembered_ssh_connect", "real_codex"}
MAILBOX = {"wt_claude_refuses_provider_writable_state", "wt_claude_scoped_mailbox"}
STATUSES = {"pass", "fail", "skip", "unsupported-on-linux"}


def utc(value):
    if not isinstance(value, str):
        raise ValueError("timestamp must be a UTC string")
    result = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None or result.utcoffset() != datetime.timedelta(0):
        raise ValueError("timestamp must be explicit UTC (Z or +00:00)")
    return result.astimezone(datetime.timezone.utc)


def timestamp(value=None):
    value = value or datetime.datetime.now(datetime.timezone.utc)
    return value.isoformat().replace("+00:00", "Z")


def read_json(path):
    if path.is_symlink() or path.stat().st_size > LOG_LIMIT:
        raise ValueError("symlinked or oversized diagnostic file: " + str(path))
    return json.loads(path.read_text())


def read_receipt(path):
    if path.is_symlink() or path.stat().st_size > LOG_LIMIT:
        raise ValueError("symlinked or oversized receipt: " + str(path))
    records = [json.loads(line) for line in path.read_text().splitlines()]
    if any(not isinstance(record, dict) for record in records):
        raise ValueError("receipt records must be objects")
    return records


def source_identity(repo):
    """Hash tracked checkout bytes, without executing a binary or contacting Git remotes."""
    def git(*args):
        return subprocess.check_output(["git", "-C", str(repo), *args], stderr=subprocess.DEVNULL,
                                       timeout=10)
    try:
        if Path(git("rev-parse", "--show-toplevel").decode().strip()).resolve() != repo.resolve():
            return {"source_commit": None, "source_sha256": None}
        commit = git("rev-parse", "HEAD").decode().strip()
        digest = hashlib.sha256()
        for raw in sorted(git("ls-files", "-z").split(b"\0")):
            if not raw:
                continue
            file = repo / raw.decode()
            data = (file.readlink().as_posix().encode() if file.is_symlink() else
                    file.read_bytes() if file.is_file() else b"<missing>")
            digest.update(raw + b"\0" + str(len(data)).encode() + b"\0" + data)
        return {"source_commit": commit, "source_sha256": digest.hexdigest()}
    except (OSError, subprocess.SubprocessError):
        return {"source_commit": None, "source_sha256": None}
