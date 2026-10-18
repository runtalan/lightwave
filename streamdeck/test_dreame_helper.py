"""Tests for the Dreame bridge, against a fake cloud. No network, no account.

Run with the Python setup-dreame.sh built (or any 3.9+; the protocol module is
not needed, the cloud is faked):

    python3 -m unittest streamdeck/test_dreame_helper.py
"""
import json
import os
import sys
import unittest
from pathlib import Path

os.environ["LIGHTWAVE_DREAME_VENDOR"] = "/nonexistent"
sys.path.insert(0, str(Path(__file__).resolve().parent / "com.dinksf.lightwave.sdPlugin" / "bin"))
import dreame_helper  # noqa: E402


class FakeCloud:
    instances = []

    def __init__(self, ok=True, state=6, battery=88):
        self.ok, self.state, self.battery = ok, state, battery
        self.verification_url = None
        self.sent = []
        self.disconnected = False
        FakeCloud.instances.append(self)

    def login(self):
        return self.ok

    def get_device_info(self):
        return {"model": "dreame.vacuum.r2228o"}

    def get_devices(self):
        return {"page": {"records": [
            {"did": 836424291, "customName": "CC", "model": "dreame.vacuum.r2228o", "online": True},
            {"did": 5, "customName": "Lamp", "model": "dreame.light.x", "online": True},
        ]}}

    def send(self, method, params):
        self.sent.append((method, params))
        if method == "get_properties":
            have = {(2, 1): self.state, (3, 1): self.battery, (2, 2): 0, (4, 2): 12, (4, 3): 8, (4, 4): 1, (4, 5): 2}
            return [{"siid": p["siid"], "piid": p["piid"], "code": 0, "value": have[(p["siid"], p["piid"])]}
                    for p in params]
        # A cleaning action moves the fake vacuum on.
        if params["aiid"] == 1 and params["siid"] == 2:
            self.state = 1
        if params["aiid"] == 2 and params["siid"] == 2:
            self.state = 3
        return {"code": 0}

    def disconnect(self):
        self.disconnected = True


def bridge(**kw):
    FakeCloud.instances.clear()
    b = dreame_helper.Bridge(factory=lambda u, p, c, d: FakeCloud(**kw))
    b.set_auth("me@example.com", "pw", "us")
    return b


class BridgeTest(unittest.TestCase):
    def setUp(self):
        # The settle pause is for the real vacuum, not the tests.
        dreame_helper.SETTLE = 0

    def test_docked_vacuum_reports_label_and_battery(self):
        out = bridge().handle({"cmd": "state", "did": "1"})
        self.assertEqual((out["label"], out["battery"], out["active"]), ("CHARGING", 88, False))

    def test_state_carries_the_current_runs_minutes_and_area(self):
        out = bridge(state=1).handle({"cmd": "state", "did": "1"})
        self.assertEqual((out["minutes"], out["area"], out["error"]), (12, 8, None))

    def test_toggle_starts_when_idle_and_pauses_when_cleaning(self):
        b = bridge()
        first = b.handle({"cmd": "toggle", "did": "1"})
        self.assertEqual((first["label"], first["active"]), ("CLEANING", True))
        second = b.handle({"cmd": "toggle", "did": "1"})
        self.assertEqual(second["label"], "PAUSED")
        actions = [p for m, p in FakeCloud.instances[0].sent if m == "action"]
        self.assertEqual([(a["siid"], a["aiid"]) for a in actions], [(2, 1), (2, 2)])

    def test_dock_sends_the_charge_action(self):
        b = bridge()
        b.handle({"cmd": "dock", "did": "1"})
        action = [p for m, p in FakeCloud.instances[0].sent if m == "action"][0]
        self.assertEqual((action["siid"], action["aiid"]), (3, 1))

    def test_room_cleaning_sends_one_segment_with_current_suction_and_water(self):
        b = bridge(state=6)
        out = b.handle({"cmd": "room", "did": "1", "room": 3, "repeat": 2})
        self.assertIsNotNone(out)
        action = [p for m, p in FakeCloud.instances[0].sent if m == "action"][0]
        self.assertEqual((action["siid"], action["aiid"]), (4, 1))
        status, props = action["in"]
        self.assertEqual((status["piid"], status["value"]), (1, 18))
        self.assertEqual(props["piid"], 10)
        self.assertEqual(json.loads(props["value"]), {"selects": [[3, 2, 1, 2, 1]]})

    def test_room_press_while_cleaning_pauses_instead(self):
        b = bridge(state=1)
        b.handle({"cmd": "room", "did": "1", "room": 3})
        actions = [p for m, p in FakeCloud.instances[0].sent if m == "action"]
        self.assertEqual([(a["siid"], a["aiid"]) for a in actions], [(2, 2)])

    def test_room_needs_a_number(self):
        with self.assertRaisesRegex(ValueError, "room number"):
            bridge().handle({"cmd": "room", "did": "1"})

    def test_session_is_reused_and_dropped_after_a_failure(self):
        b = bridge()
        b.handle({"cmd": "state", "did": "1"})
        b.handle({"cmd": "state", "did": "1"})
        self.assertEqual(len(FakeCloud.instances), 1)
        first = FakeCloud.instances[0]
        first.send = lambda *a: None  # the cloud stops answering
        with self.assertRaises(RuntimeError):
            b.handle({"cmd": "state", "did": "1"})
        self.assertTrue(first.disconnected)
        b.handle({"cmd": "state", "did": "1"})
        self.assertEqual(len(FakeCloud.instances), 2)

    def test_wrong_login_says_so(self):
        b = bridge(ok=False)
        with self.assertRaisesRegex(Exception, "login rejected"):
            b.handle({"cmd": "state", "did": "1"})

    def test_devices_lists_only_vacuums(self):
        out = bridge().handle({"cmd": "devices"})
        self.assertEqual(out["vacuums"], [{"did": "836424291", "name": "CC", "model": "dreame.vacuum.r2228o", "online": True}])

    def test_serve_replies_by_id(self):
        out = []
        dreame_helper.serve(bridge(), [json.dumps({"id": 7, "cmd": "state", "did": "1"}),
                                       json.dumps({"id": 8, "cmd": "state"})], out.append)
        by = {r["id"]: r for r in out}
        self.assertTrue(by[7]["ok"])
        self.assertFalse(by[8]["ok"])
        self.assertIn("no vacuum", by[8]["error"])


if __name__ == "__main__":
    unittest.main()
