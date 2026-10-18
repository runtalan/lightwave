"""Check that this Mac can sign in to the Dreame cloud and see your vacuum.

Run in your own terminal, with the Python that setup-dreame.sh built:

    ~/Library/Application\\ Support/Lightwave/dreame/venv/bin/python streamdeck/dreame_probe.py

It asks for the email and password of your Dreamehome app account (typed
hidden, never stored or printed), signs in, and lists the vacuums on the
account with their model and device id. It does not start, stop or change
anything. If it works, a Stream Deck key can be built on top; if it fails, the
message says where.
"""
import argparse
import getpass
import os
import sys

VENDOR = os.path.expanduser("~/Library/Application Support/Lightwave/dreame/vendor")
sys.path.insert(0, VENDOR)

from dreame.protocol import DreameVacuumDreameHomeCloudProtocol  # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--country", default="us", help="Dreame region: us, eu, cn, ru, sg ... (default us)")
    args = ap.parse_args()

    user = input("Dreamehome email: ").strip()
    password = getpass.getpass("Dreamehome password: ")

    cloud = DreameVacuumDreameHomeCloudProtocol(user, password, "dreame", args.country)
    if not cloud.login():
        print("\nSign-in failed.")
        if cloud.verification_url:
            print("Dreame asks for verification:", cloud.verification_url)
        elif cloud.captcha_img:
            print("Dreame asks for a captcha, which this probe cannot answer.")
        else:
            print("Wrong email/password, or the wrong region. Try --country eu (or us, sg, cn).")
        sys.exit(1)

    print("\nSigned in. Vacuums on this account:")
    devices = cloud.get_devices() or {}
    records = devices.get("page", {}).get("records", []) if isinstance(devices, dict) else []
    if not records:
        print("  none found (is the vacuum in the Dreamehome app, not only in Google Home?)")
    for d in records:
        print("  %s  model=%s  did=%s  online=%s" % (
            d.get("customName") or (d.get("deviceInfo") or {}).get("displayName") or "(unnamed)", d.get("model"), d.get("did"), d.get("online")))
    cloud.disconnect()


if __name__ == "__main__":
    main()
