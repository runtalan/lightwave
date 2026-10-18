"""Tests for the Tapo bridge's request handling, against fake plugs.

No network and no real plug: the bridge's discover hook is swapped for one that
returns fakes, so this checks the protocol and the reconnect behaviour, not
python-kasa itself. Run with the same Python the plugin uses:

    ~/Library/Application\\ Support/Lightwave/tapo-venv/bin/python -m unittest streamdeck/test_tapo_helper.py
"""
import asyncio
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "com.dinksf.lightwave.sdPlugin" / "bin"))
import tapo_helper  # noqa: E402
from kasa import Module  # noqa: E402


class FakeEnergy:
    def __init__(self, watts):
        self.current_consumption = watts


class FakePlug:
    def __init__(self, on=False, watts=0.0, energy=True):
        self.is_on = on
        self.alias = "Desk"
        self.watts = watts
        self.modules = {Module.Energy: FakeEnergy(watts)} if energy else {}
        self.fail = None
        self.disconnected = False

    async def update(self):
        if self.fail:
            raise self.fail

    async def turn_on(self):
        self.is_on = True

    async def turn_off(self):
        self.is_on = False

    async def disconnect(self):
        self.disconnected = True


def run(coro):
    return asyncio.run(coro)


class BridgeTest(unittest.TestCase):
    def bridge(self, plug):
        opened = []

        async def discover(host):
            opened.append(host)
            return plug

        b = tapo_helper.Bridge(discover=discover)
        b.opened = opened
        return b

    def test_state_reports_power_and_rounds_it(self):
        b = self.bridge(FakePlug(on=True, watts=41.234))
        out = run(b.handle({"cmd": "state", "host": "10.0.0.5"}))
        self.assertEqual(out, {"on": True, "watts": 41.2, "alias": "Desk"})

    def test_plug_without_energy_monitoring_has_no_watts(self):
        b = self.bridge(FakePlug(on=True, energy=False))
        out = run(b.handle({"cmd": "state", "host": "10.0.0.5"}))
        self.assertIsNone(out["watts"])

    def test_toggle_flips_and_set_is_explicit(self):
        plug = FakePlug(on=False)
        b = self.bridge(plug)

        async def go():
            a = await b.handle({"cmd": "toggle", "host": "h"})
            c = await b.handle({"cmd": "set", "host": "h", "on": True})
            d = await b.handle({"cmd": "set", "host": "h", "on": False})
            return a["on"], c["on"], d["on"]

        self.assertEqual(run(go()), (True, True, False))

    def test_connection_is_reused_until_it_fails(self):
        plug = FakePlug(on=True)
        b = self.bridge(plug)

        async def go():
            await b.handle({"cmd": "state", "host": "h"})
            await b.handle({"cmd": "state", "host": "h"})
            plug.fail = RuntimeError("timed out")
            with self.assertRaises(RuntimeError):
                await b.handle({"cmd": "state", "host": "h"})
            plug.fail = None
            await b.handle({"cmd": "state", "host": "h"})

        run(go())
        self.assertEqual(b.opened, ["h", "h"])  # reopened once, after the failure
        self.assertTrue(plug.disconnected)

    def test_missing_host_is_an_error(self):
        b = self.bridge(FakePlug())
        with self.assertRaises(ValueError):
            run(b.handle({"cmd": "state", "host": " "}))

    def test_new_login_drops_open_sessions(self):
        plug = FakePlug()
        b = self.bridge(plug)

        async def go():
            await b.set_auth("a@example.com", "one")
            await b.handle({"cmd": "state", "host": "h"})
            await b.set_auth("a@example.com", "one")  # unchanged: keep the session
            self.assertIn("h", b.devices)
            await b.set_auth("a@example.com", "two")
            self.assertNotIn("h", b.devices)

        run(go())

    def test_serve_matches_replies_to_ids_and_reports_errors(self):
        b = self.bridge(FakePlug(on=True, watts=3.0))
        out = []

        async def go():
            q = asyncio.Queue()
            for req in (
                {"id": 1, "cmd": "state", "host": "h"},
                {"id": 2, "cmd": "state", "host": ""},
                {"id": 3, "cmd": "bogus", "host": "h"},
            ):
                q.put_nowait(json.dumps(req))
            q.put_nowait("not json")
            q.put_nowait(None)
            await tapo_helper.serve(b, q, out.append)

        run(go())
        by_id = {r["id"]: r for r in out}
        self.assertTrue(by_id[1]["ok"])
        self.assertEqual(by_id[1]["result"]["watts"], 3.0)
        self.assertFalse(by_id[2]["ok"])
        self.assertIn("no plug address", by_id[2]["error"])
        self.assertFalse(by_id[3]["ok"])
        self.assertEqual(len(out), 3)

    def test_tpap_wrong_account_reads_as_a_login_problem(self):
        class Failing(FakePlug):
            async def update(self):
                raise RuntimeError("TPAP discover failed for h: ACCOUNT_ERROR(-2101) (error_code=ACCOUNT_ERROR)")

        b = self.bridge(Failing())
        out = []

        async def go():
            q = asyncio.Queue()
            q.put_nowait(json.dumps({"id": 1, "cmd": "state", "host": "h"}))
            q.put_nowait(None)
            await tapo_helper.serve(b, q, out.append)

        run(go())
        self.assertFalse(out[0]["ok"])
        self.assertIn("login rejected", out[0]["error"])


if __name__ == "__main__":
    unittest.main()
