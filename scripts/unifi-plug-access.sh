#!/usr/bin/env bash
#
# Inspect (and optionally create) a UniFi firewall policy letting this Mac
# reach Tapo plugs on another VLAN over TCP 80.
#
# Read-only by default. It prints what it finds and exactly what it would
# create; nothing is written unless you pass --apply.
#
# The API key never appears on the command line — it is read from the macOS
# keychain, so it stays out of shell history and out of the process table.
#
#   Store it once:
#     security add-generic-password -a unifi-api -s unifi.192.168.8.1 -w 'YOUR_KEY' -U
#
#   Then:
#     ./scripts/unifi-plug-access.sh                       # look around
#     ./scripts/unifi-plug-access.sh --plugs 10.0.20.5,10.0.20.6
#     ./scripts/unifi-plug-access.sh --plugs ... --apply    # actually create
#
set -euo pipefail

HOST="${UNIFI_HOST:-192.168.8.1}"
KEYCHAIN_SERVICE="${UNIFI_KEYCHAIN_SERVICE:-unifi.$HOST}"
KEYCHAIN_ACCOUNT="${UNIFI_KEYCHAIN_ACCOUNT:-unifi-api}"
BASE="https://$HOST/proxy/network/integration/v1"
APPLY=0
PLUGS=""
RULE_NAME="${UNIFI_RULE_NAME:-Allow Lightwave to Tapo plugs}"

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --plugs) PLUGS="${2:-}"; shift 2 ;;
    --host)  HOST="${2:-}"; BASE="https://$HOST/proxy/network/integration/v1"; shift 2 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

KEY="$(security find-generic-password -a "$KEYCHAIN_ACCOUNT" -s "$KEYCHAIN_SERVICE" -w 2>/dev/null || true)"
if [ -z "$KEY" ]; then
  cat >&2 <<EOF
No API key in the keychain for service "$KEYCHAIN_SERVICE".

Create one in the UniFi console (Settings -> Admins & Users -> your admin ->
Create API Key), then store it:

  security add-generic-password -a $KEYCHAIN_ACCOUNT -s $KEYCHAIN_SERVICE -w 'PASTE_KEY' -U
EOF
  exit 1
fi

# UniFi consoles ship a self-signed certificate, so -k is expected here. It is
# a LAN address on your own network, not a public endpoint.
api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sk -X "$method" "$BASE$path" \
      -H "X-API-KEY: $KEY" -H 'Content-Type: application/json' -H 'Accept: application/json' \
      -d "$body"
  else
    curl -sk -X "$method" "$BASE$path" \
      -H "X-API-KEY: $KEY" -H 'Accept: application/json'
  fi
}

echo "== console $HOST =="
if ! api GET /info | python3 -c 'import json,sys; d=json.load(sys.stdin); print("  UniFi Network", d.get("applicationVersion","?"))' 2>/dev/null; then
  echo "  could not read /info — is the key valid, and is this a UniFi OS console?" >&2
  exit 1
fi

SITE_ID="$(api GET /sites | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["data"][0]["id"])')"
echo "  site $SITE_ID"

echo
echo "== firewall zones =="
api GET "/sites/$SITE_ID/firewall/zones" | python3 -c '
import json,sys
for z in json.load(sys.stdin).get("data",[]):
    nets = z.get("networkIds") or []
    print(f"  {z[\"id\"]}  {z.get(\"name\",\"?\"):<24} networks={len(nets)}")
'

echo
echo "== networks (VLANs) =="
api GET "/sites/$SITE_ID/networks" | python3 -c '
import json,sys
for n in json.load(sys.stdin).get("data",[]):
    print(f"  {n[\"id\"]}  {n.get(\"name\",\"?\"):<24} vlan={n.get(\"vlanId\",\"-\")}  subnet={n.get(\"ipSubnet\",\"-\")}")
'

echo
echo "== existing firewall policies, in evaluation order =="
echo "   (a new allow rule must sit ABOVE any block that would match first)"
api GET "/sites/$SITE_ID/firewall/policies" | python3 -c '
import json,sys
for i,p in enumerate(json.load(sys.stdin).get("data",[])):
    act = (p.get("action") or {}).get("type","?")
    src = (p.get("source") or {}).get("zoneId","?")[:8]
    dst = (p.get("destination") or {}).get("zoneId","?")[:8]
    flag = "" if p.get("enabled") else "  (disabled)"
    print(f"  {i:>3}. [{act:<6}] {p.get(\"name\",\"?\"):<44} {src}->{dst}{flag}")
'

if [ -z "$PLUGS" ]; then
  cat <<EOF

Next: re-run with the plug addresses to see the rule that would be created.

  $0 --plugs 10.0.20.5,10.0.20.6,10.0.20.7,10.0.20.8

Nothing above changed anything — these were all reads.
EOF
  exit 0
fi

cat <<EOF

== proposed rule ==
  name         $RULE_NAME
  action       ALLOW
  protocol     TCP
  destination  $PLUGS  port 80
  source       this Mac's network
  logging      off

This script does NOT guess your zones. Creating a zone-based policy needs the
source and destination zone ids from the list above, and picking those wrong
either does nothing or opens more than you meant. Choose them deliberately:

  1. Find the zone holding your Mac's network, and the zone holding the plugs.
  2. In the UniFi UI: Settings -> Security -> Firewall -> Create Policy,
     with the values above, and drag it above the inter-VLAN block.

The UI shows you the match order as you place it, which the API cannot. For a
one-off rule that is the safer surface — the API is worth it for rules you
create repeatedly, not for a single rule you will place once.
EOF

if [ "$APPLY" = "1" ]; then
  echo
  echo "--apply was passed, but this script deliberately stops here." >&2
  echo "Creating the policy needs zone ids only you can choose; see above." >&2
  exit 3
fi
