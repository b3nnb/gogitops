import json, sys
try:
    d = json.load(open(sys.argv[1]))
except:
    print("⚪ gogitops")
    sys.exit()

host = d.get("hostname", "?")
neb = d.get("nebula_running", False)
svc_ok = d.get("services_healthy", False)
svc_down = d.get("services_down") or []
peers_total = d.get("peers_total", 0)
peers_healthy = d.get("peers_healthy", 0)

all_up = svc_ok and len(svc_down) == 0
icon = "\U0001f7e2" if all_up else "\U0001f534"
out = f"{icon} {host}"

if svc_down:
    out += f" \u2193{','.join(svc_down[:3])}"
    if len(svc_down) > 3:
        out += f"+{len(svc_down)-3}"
if not neb:
    out += " neb\u2717"

sys.stdout.write(out)
