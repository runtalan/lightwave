#!/usr/bin/env python3
"""Generate an importable Stream Deck profile for the Lightwave plugin.

Layout is read from the running Lightwave app so the profile is populated with
the user's actual lights and names. Falls back to a generic 1-9 layout if the
app is not running.
"""
import json, os, shutil, socket, sys, uuid, zipfile

PLUGIN = "com.dinksf.lightwave"
# Hardware model code as Stream Deck writes it, not the marketing name: the
# app matches a profile to a device by this string. 20GBJ9901 = Stream Deck Neo.
DEVICE_MODEL = "20GBJ9901"
COLS, ROWS = 4, 2

def lightwave_pads():
    try:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(2); s.connect("/tmp/lightwave.sock")
        s.sendall(b"STATE\n")
        line = s.recv(65536).decode().strip(); s.close()
        if line.startswith("STATE "):
            return [p for p in json.loads(line[6:])["pads"] if p.get("bound")]
    except Exception:
        pass
    return []

def action(uuid_suffix, name, settings=None, states=1):
    return {
        "ActionID": str(uuid.uuid4()),
        "LinkedTitle": False,
        "Name": name,
        "Plugin": {"Name": "Lightwave", "UUID": PLUGIN, "Version": "1.0.0.0"},
        "Resources": None,
        "Settings": settings or {},
        "State": 0,
        "States": [{} for _ in range(states)],
        "UUID": f"{PLUGIN}.{uuid_suffix}",
    }

def page(actions):
    return {"Controllers": [{"Actions": actions, "Type": "Keypad"}], "Icon": "", "Name": ""}

def build(dest_dir, out_file, personal=False):
    """Build a profile. By default keys are labelled generically ("Light 1")
    so the file is safe to publish; pass --mine to bake in the names of the
    lights currently bound in Lightwave."""
    pads = lightwave_pads() if personal else []
    if not pads:
        pads = [{"n": n, "name": f"Light {n}"} for n in (1, 2, 3, 4, 5, 6, 7, 8)]
        if personal:
            print("lightwave not reachable — using generic pad layout")

    # Page 1: first four lights on the top row, controls beneath.
    p1, p2 = {}, {}
    first, rest = pads[:4], pads[4:]
    for i, pad in enumerate(first):
        p1[f"{i},0"] = action("pad", pad["name"], {"pad": pad["n"]}, states=2)
    # Status leads the control row: it is the one key you read rather than
    # press, so it belongs on the page you land on, not behind a page turn. It
    # also reports brightness, which is why no separate readout sits here.
    p1["0,1"] = action("status", "Status")
    p1["1,1"] = action("alloff", "All Lights", states=2)
    p1["2,1"] = action("gradient", "Pattern", states=2)
    p1["3,1"] = action("dance", "Color Fade", states=2)

    # Page 2 is the scene page: remaining lights up top, then the palette
    # controls, the brightness readout, and an All Lights within reach so an
    # emergency off never needs a page turn.
    for i, pad in enumerate(rest[:4]):
        p2[f"{i},0"] = action("pad", pad["name"], {"pad": pad["n"]}, states=2)
    p2["0,1"] = action("palette", "Palette −", {"direction": "prev"})
    p2["1,1"] = action("palette", "Palette +", {"direction": "next"})
    p2["2,1"] = action("brightness", "Brightness")
    p2["3,1"] = action("alloff", "All Lights", states=2)

    id1, id2 = str(uuid.uuid4()).upper(), str(uuid.uuid4()).upper()
    prof_id = str(uuid.uuid4()).upper()
    root = os.path.join(dest_dir, f"{prof_id}.sdProfile")
    if os.path.exists(root):
        shutil.rmtree(root)
    for pid, data in ((id1, p1), (id2, p2)):
        d = os.path.join(root, "Profiles", pid)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "manifest.json"), "w") as f:
            json.dump(page(data), f, indent=2)

    manifest = {
        # UUID empty so the profile imports against whichever Neo is attached
        # rather than being pinned to one serial number.
        "Device": {"Model": DEVICE_MODEL, "UUID": ""},
        "Name": "Lightwave",
        "Pages": {"Current": id1.lower(), "Default": id1.lower(),
                  "Pages": [id1.lower(), id2.lower()]},
        "Version": "3.0",
    }
    with open(os.path.join(root, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2)

    # .streamDeckProfile is a zip of the .sdProfile directory.
    if os.path.exists(out_file):
        os.remove(out_file)
    with zipfile.ZipFile(out_file, "w", zipfile.ZIP_DEFLATED) as z:
        for base, _, files in os.walk(root):
            for fn in files:
                full = os.path.join(base, fn)
                rel = os.path.relpath(full, dest_dir)
                z.write(full, rel)
    shutil.rmtree(root)
    names = [p["name"] for p in pads]
    print(f"profile: {out_file}")
    print(f"  page 1: {', '.join(n for n in names[:4])} + Status, All Lights, Pattern, Color Fade")
    if rest:
        print(f"  page 2: {', '.join(n for n in names[4:8])} + Palette -/+, Brightness, All Lights")

if __name__ == "__main__":
    here = os.path.dirname(os.path.abspath(__file__))
    personal = "--mine" in sys.argv
    out = "Lightwave-MyLights.streamDeckProfile" if personal else "Lightwave.streamDeckProfile"
    build(here, os.path.join(here, out), personal=personal)
