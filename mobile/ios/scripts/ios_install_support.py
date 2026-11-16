#!/usr/bin/env python3
"""Read-only parsers and bounded identity checks for the personal installer."""
import base64
import http.client
import json
import ipaddress
import os
from pathlib import Path
import plistlib
import re
import stat
import sys
import time
import urllib.parse
import urllib.request


def connected_iphones(document):
    result = []
    for device in document.get("result", {}).get("devices", []):
        hardware = device.get("hardwareProperties", {})
        connection = device.get("connectionProperties", {})
        properties = device.get("deviceProperties", {})
        if not hardware.get("productType", "").startswith("iPhone"):
            continue
        if connection.get("pairingState") != "paired":
            continue
        if connection.get("transportType", "").lower() not in {"local", "usb", "wired"}:
            continue
        result.append((device["identifier"], hardware.get("udid", ""),
                       properties.get("name", "iPhone"), properties.get("developerModeStatus", "unknown")))
    return result


def signing_teams(preferences):
    teams = set()

    def visit(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key.lower() in {"teamid", "teamidentifier", "development_team"}:
                    if isinstance(item, str) and re.fullmatch(r"[A-Z0-9]{10}", item):
                        teams.add(item)
                visit(item)
        elif isinstance(value, list):
            for item in value:
                visit(item)

    # Restrict discovery to provisioning metadata, never account tokens.
    visit(preferences.get("IDEProvisioningTeams", {}))
    selected = preferences.get("IDEProvisioningTeamManagerLastSelectedTeamID")
    if isinstance(selected, str) and re.fullmatch(r"[A-Z0-9]{10}", selected):
        teams.add(selected)
    return sorted(teams)


def running_mode(directory):
    """Ask the live owner of the private control token; never infer from files."""
    try:
        control = json.loads((Path(directory) / "phone-control.json").read_text())
        port, token, wing = control["port"], control["token"], control["wing_id"]
        if (type(port) is not int or not 1 <= port <= 65535
                or not isinstance(token, str) or not token or not isinstance(wing, str) or not wing):
            return None
        address = control.get("address", "")
        host = urllib.parse.urlsplit("http://" + address).hostname if address else "127.0.0.1"
        ip = ipaddress.ip_address(host)
        if ip.is_unspecified:
            host = "127.0.0.1" if ip.version == 4 else "::1"
        if ":" in host:
            host = "[" + host + "]"
        url = "http://" + host + ":" + str(port) + "/admin"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirects())
        request = urllib.request.Request(url, data=b'{"Op":"mode"}',
                                         headers={"Authorization": "Bearer " + token})
        with opener.open(request, timeout=2) as response:
            data = response.read(4097)
            if response.status != 200 or response.url != url or len(data) > 4096:
                return None
            reply = json.loads(data)
        if reply.get("wing_id") == wing and reply.get("mode") in {"local-only", "relay"}:
            return reply["mode"]
    except (OSError, ValueError, TypeError, KeyError, AttributeError, http.client.HTTPException):
        pass
    return None


def check_pair_output(path):
    """Refuse scripts, links and other artifacts before Go tries to overwrite them."""
    path = Path(path)
    if not path.exists() and not path.is_symlink():
        return
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode & 0o111:
        raise ValueError()
    with path.open("rb") as binary:
        magic = binary.read(4)
    # Mach-O (including universal binaries) and ELF for portable fixtures.
    if magic not in {b"\x7fELF", b"\xcf\xfa\xed\xfe", b"\xfe\xed\xfa\xcf",
                     b"\xce\xfa\xed\xfe", b"\xfe\xed\xfa\xce", b"\xca\xfe\xba\xbe",
                     b"\xbe\xba\xfe\xca", b"\xca\xfe\xba\xbf", b"\xbf\xba\xfe\xca"}:
        raise ValueError()


def legacy_restart_mode(status):
    """Use the mode reported after wing status verified the live daemon PID."""
    lines = status.splitlines()
    if not lines or not re.fullmatch(r"wing daemon is running \(pid [1-9][0-9]*\)", lines[0]):
        return None
    local = "  local control: ready" in lines
    relay = any(line.startswith("  relay: ") for line in lines)
    if local != relay:
        return "local-only" if local else "relay"
    return None


def setup_fields(text):
    if len(text.encode()) > 16384 or re.search(r"%(?![0-9a-fA-F]{2})", text):
        raise ValueError()
    parts = urllib.parse.urlsplit(text.strip())
    if parts.scheme != "wingthing" or parts.netloc != "home" or parts.path or parts.fragment:
        raise ValueError()
    pairs = urllib.parse.parse_qsl(parts.query.replace("+", "%2B"), keep_blank_values=True, strict_parsing=True)
    fields = dict(pairs)
    if len(pairs) != len(fields) or set(fields) != {"v", "origin", "account", "wing", "key", "token"}:
        raise ValueError()
    origin = urllib.parse.urlsplit(fields["origin"])
    if (fields["v"] != "1" or origin.scheme != "https" or not origin.hostname
            or origin.username is not None or origin.password is not None
            or origin.path not in {"", "/"} or "?" in fields["origin"] or "#" in fields["origin"]):
        raise ValueError()
    if origin.port is not None and not 1 <= origin.port <= 65535:
        raise ValueError()
    for name in ("account", "wing", "token"):
        if not fields[name].strip() or any(ord(c) < 32 or ord(c) == 127 for c in fields[name]):
            raise ValueError()
    if len(base64.b64decode(fields["key"], validate=True)) != 32:
        raise ValueError()
    return fields


def read_setup(path):
    path = Path(path)
    stat = path.lstat()
    if path.is_symlink() or not path.is_file() or stat.st_uid != os.getuid() or stat.st_mode & 0o077:
        raise ValueError()
    if stat.st_size > 16384:
        raise ValueError()
    return setup_fields(path.read_text())


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def verify_home(fields, timeout=60):
    opener = urllib.request.build_opener(NoRedirects())
    origin = fields["origin"].rstrip("/")
    deadline = time.monotonic() + timeout

    def get(path):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError()
        request = urllib.request.Request(origin + path, headers={"Authorization": "Bearer " + fields["token"]})
        with opener.open(request, timeout=min(10, remaining)) as response:
            data = response.read((2 << 20) + 1)
            if response.status != 200 or response.url != origin + path or len(data) > 2 << 20:
                raise ValueError()
            return json.loads(data)

    health, account = get("/health"), get("/auth/check")
    if health.get("ok") is not True or account.get("ok") is not True or account.get("user_id") != fields["account"]:
        raise ValueError()
    delay = 1
    while True:
        roster = get("/api/app/wings")
        if not isinstance(roster, list):
            raise ValueError()
        matches = [wing for wing in roster if wing.get("wing_id") == fields["wing"]]
        if len(matches) == 1:
            if matches[0].get("public_key") != fields["key"]:
                raise ValueError()
            return
        if matches or time.monotonic() >= deadline:
            raise ValueError()
        time.sleep(min(delay, max(0, deadline - time.monotonic())))
        delay = min(delay * 2, 8)


def main():
    command, path, *extra = sys.argv[1:]
    if command == "lan-restart":
        print(running_mode(path) or "unknown")
    elif command == "legacy-restart-mode":
        print(legacy_restart_mode(Path(path).read_text()) or "unknown")
    elif command == "pair-output":
        check_pair_output(path)
    elif command == "file-proxy":
        print(Path(path).resolve().as_uri())
    elif command == "devices":
        phones = connected_iphones(json.loads(Path(path).read_text()))
        requested = extra[0] if extra else ""
        for identifier, udid, name, mode in phones:
            if not requested or requested in {identifier, udid}:
                print("\t".join([identifier, name.replace("\t", " ").replace("\n", " "), mode]))
    elif command == "teams":
        for team in signing_teams(plistlib.loads(Path(path).read_bytes())):
            print(team)
    elif command in {"validate", "verify", "identity"}:
        fields = read_setup(path)
        if command == "verify":
            verify_home(fields)
        elif command == "identity":
            del fields["token"]
            print("wingthing://home?" + urllib.parse.urlencode(fields, quote_via=urllib.parse.quote))
    else:
        raise ValueError()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never echo a URL, HTTP body, token, or parse exception.
        print("STOP: Device/team metadata or private Home identity check failed.", file=sys.stderr)
        sys.exit(1)
