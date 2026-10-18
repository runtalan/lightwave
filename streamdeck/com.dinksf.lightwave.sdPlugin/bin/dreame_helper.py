#!/usr/bin/env python3
"""Dreame vacuum bridge for the Lightwave Stream Deck plugin.

The plugin is Go, and Dreame's cloud API is reverse-engineered and kept working
by Tasshack/dreame-vacuum (MIT), whose protocol module setup-dreame.sh fetches
at a pinned commit. This script runs under the Python that setup built, and
speaks JSON lines on stdin/stdout, one request and one reply per line, matched
by "id":

    {"id": 1, "cmd": "auth", "username": "...", "password": "...", "country": "us"}
    {"id": 2, "cmd": "devices"}
    {"id": 3, "cmd": "state",  "did": "836424291"}
    {"id": 4, "cmd": "toggle", "did": "836424291"}     # also: start, pause, dock
    {"id": 5, "cmd": "room",   "did": "836424291", "room": 3, "repeat": 1}

    {"id": 3, "ok": true, "result": {"label": "DOCKED", "active": false, "battery": 100}}
    {"id": 3, "ok": false, "error": "..."}

Requests run on a thread pool (the protocol client is blocking), and each
vacuum's session is used by one request at a time. A session that errors is
dropped and signed in again on the next request. Diagnostics go to stderr: the
reply stream must carry nothing else.
"""
import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

# setup-dreame.sh puts the vendored protocol module beside the venv it builds.
sys.path.insert(0, os.environ.get("LIGHTWAVE_DREAME_VENDOR") or str(Path(sys.prefix).parent / "vendor"))

try:
    from dreame.protocol import DreameVacuumDreameHomeCloudProtocol
    IMPORT_ERROR = ""
except ImportError as exc:  # pragma: no cover - reported to the plugin
    DreameVacuumDreameHomeCloudProtocol = None
    IMPORT_ERROR = str(exc)

# MIoT ids, from the Dreame vacuum spec the upstream project maps.
STATE = {"siid": 2, "piid": 1}
BATTERY = {"siid": 3, "piid": 1}
ERROR = {"siid": 2, "piid": 2}
CLEAN_MINUTES = {"siid": 4, "piid": 2}
CLEAN_AREA = {"siid": 4, "piid": 3}
SUCTION = {"siid": 4, "piid": 4}
WATER = {"siid": 4, "piid": 5}
# "Custom start": the one action behind room cleaning. Its input is the status
# to start in (segment cleaning) and a JSON string of what to clean.
START_CUSTOM = {"siid": 4, "aiid": 1}
STATUS_PIID = 1
CLEANING_PROPERTIES_PIID = 10
SEGMENT_CLEANING = 18
ACTIONS = {
    "start": {"siid": 2, "aiid": 1},  # start, or resume when paused
    "pause": {"siid": 2, "aiid": 2},
    "dock": {"siid": 3, "aiid": 1},   # "charge": go back to the dock
}

# What the vacuum's state code means, as a word short enough for a key.
LABELS = {
    1: "CLEANING", 7: "CLEANING", 12: "CLEANING", 15: "CLEANING",
    2: "IDLE", 3: "PAUSED", 4: "ERROR",
    5: "RETURNING", 10: "RETURNING", 17: "RETURNING", 18: "RETURNING",
    6: "CHARGING", 13: "DOCKED",
    8: "DRYING", 9: "WASHING", 21: "WASHING", 22: "EMPTYING",
    11: "MAPPING", 14: "UPDATING",
}
# How long to let the vacuum react to a command before reporting its state.
SETTLE = 1.5

# States where a press should pause, rather than start.
ACTIVE = {"CLEANING", "RETURNING"}


class LoginError(Exception):
    pass


def describe(props):
    """Turn a get_properties result into what the key shows."""
    by = {(p.get("siid"), p.get("piid")): p for p in props or [] if p.get("code", 0) == 0}
    state = by.get((STATE["siid"], STATE["piid"]), {}).get("value")
    battery = by.get((BATTERY["siid"], BATTERY["piid"]), {}).get("value")
    error = by.get((ERROR["siid"], ERROR["piid"]), {}).get("value")
    minutes = by.get((CLEAN_MINUTES["siid"], CLEAN_MINUTES["piid"]), {}).get("value")
    area = by.get((CLEAN_AREA["siid"], CLEAN_AREA["piid"]), {}).get("value")
    if state is None:
        raise RuntimeError("the vacuum did not report its state")
    label = LABELS.get(state, "BUSY")
    return {
        "state": state,
        "label": label,
        "active": label in ACTIVE,
        "battery": battery if isinstance(battery, int) else None,
        # The current (or last) run: minutes and square metres cleaned.
        "minutes": minutes if isinstance(minutes, int) else None,
        "area": area if isinstance(area, int) else None,
        # Non-zero while the vacuum reports a fault.
        "error": error if isinstance(error, int) and error != 0 else None,
    }


class Bridge:
    def __init__(self, factory=None):
        self.creds = None
        self.sessions = {}
        self.locks = {}
        self.guard = threading.Lock()
        self.factory = factory or (lambda user, pw, country, did: DreameVacuumDreameHomeCloudProtocol(user, pw, "dreame", country, did=did))

    def handle(self, req):
        cmd = req.get("cmd")
        if cmd == "auth":
            self.set_auth(req.get("username") or "", req.get("password") or "", req.get("country") or "us")
            return {}
        if cmd == "devices":
            return {"vacuums": self.devices()}
        did = str(req.get("did") or "").strip()
        if not did:
            raise ValueError("no vacuum chosen")
        if cmd not in ("state", "toggle", "room", *ACTIONS):
            raise ValueError("unknown command " + str(cmd))
        with self.guard:
            lock = self.locks.setdefault(did, threading.Lock())
        with lock:
            try:
                cloud = self.session(did)
                if cmd == "room":
                    # Like toggle: a press while it is working pauses it.
                    if self.read(cloud, did)["active"]:
                        cmd = "pause"
                    else:
                        self.clean_room(cloud, did, req)
                        time.sleep(SETTLE)
                        return self.read(cloud, did)
                if cmd == "toggle":
                    cmd = "pause" if self.read(cloud, did)["active"] else "start"
                if cmd in ACTIONS:
                    self.act(cloud, did, cmd)
                    # Give the vacuum a moment to move, so the reply is the new state.
                    time.sleep(SETTLE)
                return self.read(cloud, did)
            except Exception:
                self.forget(did)
                raise

    def set_auth(self, user, password, country):
        creds = (user, password, country) if user and password else None
        if creds != self.creds:
            for did in list(self.sessions):
                self.forget(did)
        self.creds = creds

    def signed_in(self, did):
        if not self.creds:
            raise LoginError("no Dreame login set")
        user, password, country = self.creds
        cloud = self.factory(user, password, country, did)
        if not cloud.login():
            if getattr(cloud, "verification_url", None):
                raise LoginError("login rejected: Dreame wants verification, sign in once in the Dreamehome app")
            raise LoginError("login rejected: check the Dreamehome email, password and region")
        return cloud

    def session(self, did):
        cloud = self.sessions.get(did)
        if cloud is None:
            cloud = self.signed_in(did)
            # Sets the device's routing host, which commands need.
            if cloud.get_device_info() is None:
                raise RuntimeError("this account has no vacuum with id " + did)
            self.sessions[did] = cloud
        return cloud

    def forget(self, did):
        cloud = self.sessions.pop(did, None)
        if cloud is not None:
            try:
                cloud.disconnect()
            except Exception:
                pass

    @staticmethod
    def read(cloud, did):
        props = cloud.send("get_properties", [{"did": did, **p} for p in (STATE, BATTERY, ERROR, CLEAN_MINUTES, CLEAN_AREA)])
        if props is None:
            raise RuntimeError("the Dreame cloud did not answer")
        return describe(props)

    @staticmethod
    def act(cloud, did, name):
        result = cloud.send("action", {"did": did, **ACTIONS[name], "in": []})
        code = result.get("code") if isinstance(result, dict) else None
        if code != 0:
            raise RuntimeError("the vacuum refused %s (code %s)" % (name, code))

    def clean_room(self, cloud, did, req):
        """Clean one room, by the number the vacuum's map gives it.

        One room per command: the order field upstream sends for several rooms
        breaks some models, and a key is one room anyway. Suction and water are
        whatever the vacuum is set to now, which the command wants spelled out.
        """
        try:
            room = int(req.get("room"))
        except (TypeError, ValueError):
            room = 0
        if room < 1:
            raise ValueError("no room number set")
        repeat = max(1, min(int(req.get("repeat") or 1), 3))
        props = cloud.send("get_properties", [{"did": did, **SUCTION}, {"did": did, **WATER}])
        by = {(p.get("siid"), p.get("piid")): p.get("value") for p in props or [] if p.get("code", 0) == 0}
        suction = by.get((SUCTION["siid"], SUCTION["piid"]), 1)
        water = by.get((WATER["siid"], WATER["piid"]), 2)
        selects = json.dumps({"selects": [[room, repeat, suction, water, 1]]}, separators=(",", ":"))
        result = cloud.send("action", {
            "did": did, **START_CUSTOM,
            "in": [
                {"piid": STATUS_PIID, "value": SEGMENT_CLEANING},
                {"piid": CLEANING_PROPERTIES_PIID, "value": selects},
            ],
        })
        code = result.get("code") if isinstance(result, dict) else None
        if code != 0:
            raise RuntimeError("the vacuum refused room %d (code %s); is that room number on its map?" % (room, code))

    def devices(self):
        cloud = self.signed_in(None)
        try:
            data = cloud.get_devices() or {}
        finally:
            try:
                cloud.disconnect()
            except Exception:
                pass
        out = []
        for d in (data.get("page") or {}).get("records") or []:
            if ".vacuum." not in str(d.get("model")):
                continue
            out.append({
                "did": str(d.get("did")),
                "name": d.get("customName") or (d.get("deviceInfo") or {}).get("displayName") or "Vacuum",
                "model": d.get("model") or "",
                "online": bool(d.get("online", True)),
            })
        return out


def serve(bridge, lines, write):
    def one(line):
        try:
            req = json.loads(line)
        except ValueError:
            return
        reply = {"id": req.get("id")}
        try:
            reply["result"] = bridge.handle(req)
            reply["ok"] = True
        except Exception as exc:
            reply["ok"], reply["error"] = False, str(exc) or exc.__class__.__name__
        write(reply)

    with ThreadPoolExecutor(max_workers=8) as pool:
        for line in lines:
            if line.strip():
                pool.submit(one, line)
    for did in list(bridge.sessions):
        bridge.forget(did)


def main():
    out_lock = threading.Lock()

    def write(reply):
        with out_lock:
            sys.stdout.write(json.dumps(reply) + "\n")
            sys.stdout.flush()

    serve(Bridge(), sys.stdin, write)


if __name__ == "__main__":
    if IMPORT_ERROR:
        sys.exit("the Dreame client is not installed: " + IMPORT_ERROR)
    main()
