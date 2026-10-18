"""Read what the Dreame cloud will tell us about where the vacuum is.

A probe, to run in your own terminal with the Python setup-dreame.sh built:

    ~/Library/Application\\ Support/Lightwave/dreame/venv/bin/python streamdeck/dreame_telemetry.py

It signs in (email and password typed hidden, never stored or printed), reads a
few status properties, asks the vacuum to upload its current map (the same
request the Dreamehome app makes when you open the map), downloads and decodes
that map, and prints: the robot's position and heading, which room it is in,
the room names stored on the map, and the names of any obstacle photos.

It starts, stops and changes nothing. Nothing it prints is secret, but it does
include your room names and where the vacuum is, so read it before you paste it.
"""
import argparse
import base64
import getpass
import hashlib
import json
import os
import sys
import time
import zlib

sys.path.insert(0, os.path.expanduser("~/Library/Application Support/Lightwave/dreame/vendor"))
from dreame.protocol import DreameVacuumDreameHomeCloudProtocol  # noqa: E402

HEADER = 27  # bytes of fixed header before the pixel grid


def i16(b, o):
    return int.from_bytes(b[o:o + 2], "little", signed=True)


def short(v, n=300):
    s = v if isinstance(v, str) else json.dumps(v, default=str)
    return s if len(s) <= n else s[:n] + "…(%d chars)" % len(s)


def object_name(cloud, did):
    """The newest map file's name, and its decryption key if it has one."""
    res = cloud.get_properties("6.3")
    print("  object-name reply:", short(res))
    val = None
    if isinstance(res, list) and res:
        val = res[0].get("value", res[0].get("val")) if isinstance(res[0], dict) else res[0]
    if isinstance(val, str):
        try:
            val = json.loads(val)
        except ValueError:
            pass
    if isinstance(val, list) and val:
        val = val[0]
    if not isinstance(val, str) or not val:
        return None, None
    name, _, key = val.partition(",")
    return name, (key or None)


def decode(text, key):
    """Undo the transport wrapping: URL-safe base64, optional AES, zlib."""
    raw = text.replace("_", "/").replace("-", "+")
    if "," in raw and key is None:
        raw, key = raw.split(",")[:2]
    data = base64.decodebytes(raw.encode())
    if key:
        try:
            from cryptography.hazmat.backends import default_backend
            from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
            k = hashlib.sha256(key.encode()).hexdigest()[0:32].encode()
            # This model has no stored IV, so try the all-zero one.
            d = Cipher(algorithms.AES(k), modes.CBC(b"\0" * 16), backend=default_backend()).decryptor()
            data = d.update(data) + d.finalize()
        except Exception as ex:
            print("  decrypt failed:", ex)
    return zlib.decompress(data)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--country", default="us")
    ap.add_argument("--did", default="", help="vacuum id (default: the first vacuum on the account)")
    ap.add_argument("--save", default="/tmp/dreame-map.bin",
                    help="where to write the decoded map, for analysis (default /tmp/dreame-map.bin)")
    args = ap.parse_args()

    user = input("Dreamehome email: ").strip()
    password = getpass.getpass("Dreamehome password: ")

    cloud = DreameVacuumDreameHomeCloudProtocol(user, password, "dreame", args.country)
    if not cloud.login():
        sys.exit("Sign-in failed.")
    did = args.did
    if not did:
        recs = (cloud.get_devices() or {}).get("page", {}).get("records", [])
        vac = [r for r in recs if ".vacuum." in str(r.get("model"))]
        if not vac:
            sys.exit("No vacuum on this account.")
        did = str(vac[0]["did"])
    cloud.disconnect()

    cloud = DreameVacuumDreameHomeCloudProtocol(user, password, "dreame", args.country, did=did)
    if not cloud.login() or cloud.get_device_info() is None:
        sys.exit("Could not open the vacuum.")

    print("\n== status ==")
    props = [{"did": did, "siid": s, "piid": p} for s, p in
             ((2, 1), (2, 2), (3, 1), (3, 2), (4, 1), (4, 2), (4, 3), (10001, 1))]
    for r in cloud.send("get_properties", props) or []:
        print("  %s.%s = %s (code %s)" % (r.get("siid"), r.get("piid"), r.get("value"), r.get("code")))

    print("\n== map ==")
    before, _ = object_name(cloud, did)
    res = cloud.send("action", {"did": did, "siid": 6, "aiid": 1,
                                "in": [{"piid": 2, "value": '{"frame_type":"I"}'}]})
    print("  asked the vacuum to upload its map:", short(res))
    name, key = before, None
    for _ in range(8):
        time.sleep(2)
        n, k = object_name(cloud, did)
        if n and n != before:
            name, key = n, k
            break
        name, key = n or name, k or key
    if not name:
        sys.exit("  no map file name came back.")
    print("  map file:", name, "| key:", "yes" if key else "no")

    url = cloud.get_interim_file_url(name)
    if not url:
        sys.exit("  no download address for the map file.")
    body = cloud.get_file(url)
    if body is None:
        sys.exit("  the map download failed.")
    text = body.decode() if isinstance(body, bytes) else body
    raw = decode(text, key)
    print("  decoded %d bytes" % len(raw))
    if args.save:
        with open(args.save, "wb") as f:
            f.write(raw)
        print("  saved the decoded map to", args.save)

    x, y, a = i16(raw, 5), i16(raw, 7), i16(raw, 9)
    cx, cy, ca = i16(raw, 11), i16(raw, 13), i16(raw, 15)
    grid, w, h, left, top = (i16(raw, o) for o in (17, 19, 21, 23, 25))
    print("\n== position ==")
    print("  robot   x=%d y=%d heading=%d" % (x, y, a))
    print("  charger x=%d y=%d heading=%d" % (cx, cy, ca))
    print("  grid=%d width=%d height=%d left=%d top=%d" % (grid, w, h, left, top))

    tail = raw[HEADER + w * h:]
    try:
        extra = json.loads(tail.decode())
    except ValueError:
        extra = {}
    if extra.get("origin"):
        left, top = extra["origin"][0], extra["origin"][1]
    print("  json keys:", sorted(extra))
    print("  format flags:", {k: extra.get(k) for k in ("fsm", "smd", "mra", "ris", "rism", "curid", "cmi", "nar", "mis")})

    gx, gy = int((x - left) / grid), int((y - top) / grid)
    px = raw[HEADER:HEADER + w * h]
    print("  robot cell: gx=%d gy=%d" % (gx, gy))
    for label, idx in (("row-major", gy * w + gx), ("column-major", gx * h + gy)):
        if 0 <= idx < len(px):
            v = px[idx]
            print("  pixel (%s) = %d -> v3 room %d, older-format room %d" % (label, v, v & 0x1F, v >> 2))
    seen = {}
    for v in px:
        seen[v & 0x1F] = seen.get(v & 0x1F, 0) + 1
    print("  room ids present in the grid (v3 reading):", sorted(k for k in seen if 0 < k < 31))

    print("\n== rooms on the map ==")
    for rid, info in sorted((extra.get("seg_inf") or {}).items(), key=lambda kv: int(kv[0])):
        nm = ""
        if info.get("name"):
            try:
                nm = base64.b64decode(info["name"]).decode()
            except Exception:
                nm = str(info["name"])
        print("  room %s  name=%r  type=%s" % (rid, nm, info.get("type")))

    print("\n== photos ==")
    for k in ("ai_obstacle", "ai_furniture", "ai_obstacle_new"):
        if extra.get(k):
            print("  %s: %s" % (k, short(extra[k])))
    print("  (none listed)" if not any(extra.get(k) for k in ("ai_obstacle", "ai_furniture", "ai_obstacle_new")) else "")
    cloud.disconnect()


if __name__ == "__main__":
    main()
