#!/usr/bin/env python3
"""Tapo plug bridge for the Lightwave Stream Deck plugin.

The plugin is Go; python-kasa (https://github.com/python-kasa/python-kasa) is
the library that actually speaks the Tapo protocols, so the plugin runs this
script as a child process and talks to it in JSON lines over stdin/stdout.

One request per line, one reply per line, matched by "id":

    {"id": 1, "cmd": "auth", "username": "...", "password": "..."}
    {"id": 2, "cmd": "discover"}
    {"id": 3, "cmd": "state",  "host": "192.168.1.40"}
    {"id": 4, "cmd": "set",    "host": "192.168.1.40", "on": true}
    {"id": 5, "cmd": "toggle", "host": "192.168.1.40"}

    {"id": 3, "ok": true, "result": {"on": true, "watts": 41.2, "alias": "Desk"}}
    {"id": 3, "ok": false, "error": "..."}

Requests run concurrently, so a slow discovery never holds up a toggle. Calls
to the same plug are serialised. A plug's session is kept across failures
other than a refused login:
python-kasa already re-handshakes after a timeout or an expired session, and
every handshake is a login the plug counts. Dropping the session too would add
a discovery and another login to each failed poll, and Tapo plugs answer a
burst of logins by refusing them for a while (STAT_ACCESS_ERROR). Only a plug
that keeps failing is dropped and found again, in case it changed protocol.

Anything the script prints other than reply lines would corrupt the stream, so
diagnostics go to stderr.
"""
import asyncio
import json
import sys
import threading

try:
    from kasa import Credentials, Discover, Module
    from kasa.exceptions import AuthenticationError
except ImportError as exc:  # pragma: no cover - reported to the plugin
    Credentials = Discover = Module = AuthenticationError = None
    IMPORT_ERROR = str(exc)
else:
    IMPORT_ERROR = ""

# How long to wait on a single plug before giving up, in seconds.
CONNECT_TIMEOUT = 8
DISCOVERY_TIMEOUT = 5
# A plug that fails this many requests in a row is dropped and discovered again.
FORGET_AFTER = 5
# How many times python-kasa retries a failed request. Each retry is a fresh
# login, and its default of three turns one bad poll into four logins.
QUERY_RETRIES = 1


def log(msg):
    print(msg, file=sys.stderr, flush=True)


class Bridge:
    def __init__(self, discover=None):
        self.credentials = None
        self.devices = {}
        self.locks = {}
        self.failures = {}
        # Injectable so the protocol handling can be tested without a network.
        self._discover = discover or self._discover_single

    async def _discover_single(self, host):
        dev = await Discover.discover_single(
            host,
            credentials=self.credentials,
            discovery_timeout=DISCOVERY_TIMEOUT,
            timeout=CONNECT_TIMEOUT,
        )
        if dev is None:
            raise RuntimeError("no device answered at " + host)
        limit_retries(dev)
        return dev

    async def handle(self, req):
        cmd = req.get("cmd")
        if cmd == "auth":
            await self.set_auth(req.get("username") or "", req.get("password") or "")
            return {}
        if cmd == "discover":
            return {"plugs": await self.discover()}
        host = (req.get("host") or "").strip()
        if not host:
            raise ValueError("no plug address set")
        async with self.locks.setdefault(host, asyncio.Lock()):
            try:
                dev = self.devices.get(host)
                if dev is None:
                    dev = await self._discover(host)
                    self.devices[host] = dev
                if cmd == "set":
                    await self.switch(dev, bool(req.get("on")))
                elif cmd == "toggle":
                    await dev.update()
                    await self.switch(dev, not dev.is_on)
                elif cmd != "state":
                    raise ValueError("unknown command " + str(cmd))
                out = await self.snapshot(dev)
            except Exception as exc:
                self.failures[host] = self.failures.get(host, 0) + 1
                # A refused login means the session is no good, perhaps opened
                # before the login arrived: start afresh with the current one.
                if self.failures[host] >= FORGET_AFTER or is_login_error(exc, str(exc)):
                    await self.forget(host)
                raise
            self.failures.pop(host, None)
            return out

    async def set_auth(self, username, password):
        creds = Credentials(username, password) if username and password else None
        if (creds is None) != (self.credentials is None) or (
            creds is not None
            and (creds.username, creds.password)
            != (self.credentials.username, self.credentials.password)
        ):
            # Open sessions were authenticated with the old login.
            for host in list(self.devices):
                await self.forget(host)
        self.credentials = creds

    async def forget(self, host):
        self.failures.pop(host, None)
        dev = self.devices.pop(host, None)
        if dev is not None:
            try:
                await dev.disconnect()
            except Exception:
                pass

    @staticmethod
    async def switch(dev, on):
        await (dev.turn_on() if on else dev.turn_off())

    @staticmethod
    async def snapshot(dev):
        await dev.update()
        energy = dev.modules.get(Module.Energy)
        watts = None
        if energy is not None:
            try:
                watts = energy.current_consumption
            except Exception:
                watts = None
        return {
            "on": bool(dev.is_on),
            "watts": None if watts is None else round(float(watts), 1),
            "alias": dev.alias or "",
        }

    async def discover(self):
        found = await Discover.discover(
            credentials=self.credentials, discovery_timeout=DISCOVERY_TIMEOUT
        )
        plugs = await asyncio.gather(*(self.describe(h, d) for h, d in found.items()))
        plugs = [p for p in plugs if p is not None]
        plugs.sort(key=lambda p: (p["alias"].lower(), p["host"]))
        return plugs

    @staticmethod
    async def describe(host, dev):
        family = dev.config.connection_type.device_family.value
        if "PLUG" not in family:
            return None  # bulbs, cameras, hubs: not what this key drives
        entry = {"host": host, "alias": "", "model": dev.model or "", "auth": True}
        try:
            await dev.update()
            entry["alias"] = dev.alias or ""
        except AuthenticationError:
            entry["auth"] = False
        except Exception as exc:
            log("discover %s: %s" % (host, exc))
        finally:
            try:
                await dev.disconnect()
            except Exception:
                pass
        return entry


def limit_retries(dev):
    """Cap python-kasa's own retries on a plug's requests (see QUERY_RETRIES)."""
    proto = getattr(dev, "protocol", None)
    query = getattr(proto, "query", None)
    if query is None:
        return

    async def limited(request, retry_count=QUERY_RETRIES):
        return await query(request, retry_count=min(retry_count, QUERY_RETRIES))

    proto.query = limited


def is_throttled(msg):
    """Whether a plug is refusing logins because it has had too many."""
    return "STAT_ACCESS_ERROR" in msg or "(-2203)" in msg


def is_login_error(exc, msg):
    """Whether a failure means the TP-Link login is wrong.

    python-kasa raises AuthenticationError for some handshakes, but the TPAP
    one reports a wrong account as a plain device error, ACCOUNT_ERROR(-2101).
    """
    return isinstance(exc, AuthenticationError) or "ACCOUNT_ERROR" in msg or "(-2101)" in msg


async def serve(bridge, lines, write):
    async def one(line):
        try:
            req = json.loads(line)
        except ValueError:
            return
        reply = {"id": req.get("id")}
        try:
            reply["result"] = await bridge.handle(req)
            reply["ok"] = True
        except Exception as exc:
            msg = str(exc) or exc.__class__.__name__
            if is_login_error(exc, msg):
                msg = "login rejected: check the TP-Link account email and password"
            elif is_throttled(msg):
                msg = "plug throttled: refusing logins after too many attempts"
            reply["ok"], reply["error"] = False, msg
        write(reply)

    tasks = set()
    while True:
        line = await lines.get()
        if line is None:
            break
        if not line.strip():
            continue
        task = asyncio.ensure_future(one(line))
        tasks.add(task)
        task.add_done_callback(tasks.discard)
    await asyncio.gather(*tasks, return_exceptions=True)
    for host in list(bridge.devices):
        await bridge.forget(host)


async def main():
    loop = asyncio.get_running_loop()
    lines = asyncio.Queue()

    def read_stdin():
        for line in sys.stdin:
            loop.call_soon_threadsafe(lines.put_nowait, line)
        loop.call_soon_threadsafe(lines.put_nowait, None)

    threading.Thread(target=read_stdin, daemon=True).start()

    def write(reply):
        sys.stdout.write(json.dumps(reply) + "\n")
        sys.stdout.flush()

    await serve(Bridge(), lines, write)


if __name__ == "__main__":
    if IMPORT_ERROR:
        sys.exit("python-kasa is not installed: " + IMPORT_ERROR)
    asyncio.run(main())
