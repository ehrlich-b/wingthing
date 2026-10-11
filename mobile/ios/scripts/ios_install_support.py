#!/usr/bin/env python3
"""Read-only parsers and bounded identity checks for the personal installer."""
import base64
import json
import os
from pathlib import Path
import plistlib
import re
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
        if connection.get("transportType", "").lower() not in {"local", "usb"}:
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
    if command == "devices":
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
