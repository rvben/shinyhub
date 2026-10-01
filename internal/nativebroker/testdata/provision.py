"""Provision only a disposable Linux integration-test VM. No client data."""
import json
import os
from pathlib import Path
import pwd
import subprocess
import sys

ROOT = Path("/var/lib/shinyhub-isolation-test")
assert sys.platform == "linux" and os.geteuid() == 0
assert not ROOT.exists(), "Refuse to overwrite an existing fixture"
reference = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[3] / "deploy/systemd/shinyhub-native-broker.service"
service = reference.read_text().replace("/etc/shinyhub/native-broker.json", "/etc/shinyhub-isolation-test.json")
for name, uid in (("shiso-control", 22100), ("shiso-alpha", 22101), ("shiso-beta", 22102)):
    try:
        pwd.getpwuid(uid)
    except KeyError:
        subprocess.run(["useradd", "--uid", str(uid), "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", name], check=True)
    else:
        raise RuntimeError("Fixture UID already exists")
subprocess.run(["usermod", "--append", "--groups", "shiso-alpha,shiso-beta", "shiso-control"], check=True)

def directory(path, uid=0, gid=0, mode=0o711):
    path.mkdir(parents=True, exist_ok=True)
    os.chown(path, uid, gid)
    os.chmod(path, mode)

directory(ROOT, 22100, 22100)
directory(ROOT / "control", 22100, 22100, 0o700)
secret = ROOT / "control/auth-secret"
secret.write_text("synthetic control-plane fixture")
os.chown(secret, 22100, 22100)
os.chmod(secret, 0o600)
apps = []
for app, number in (("alpha", 22101), ("beta", 22102)):
    for tree in ("apps", "appdata", "appcache"):
        directory(ROOT / tree, 22100, 22100)
    directory(ROOT / "apps" / app, 22100, number, 0o750)
    bundle = ROOT / "apps" / app / "versions"
    data = ROOT / "appdata" / app
    cache = ROOT / "appcache" / app
    for path in (bundle, data, cache):
        directory(path, 22100, number, 0o750)
    for version in ("v1", "v2"):
        directory(bundle / version, 22100, number, 0o750)
    apps.append({"id": number - 22100, "slug": app, "uid": number, "gid": number,
                 "bundle_root": str(bundle), "data_root": str(data), "cache_root": str(cache)})
policy = {"control_uid": 22100, "control_gid": 22100,
          "socket": "/run/shinyhub-isolation-test/control.sock",
          "runtime_dir": "/run/shinyhub-isolation-test",
          "state_dir": "/var/lib/shinyhub-isolation-test-broker", "apps": apps}
path = Path("/etc/shinyhub-isolation-test.json")
path.write_text(json.dumps(policy))
os.chmod(path, 0o600)
unit = Path("/etc/systemd/system/shinyhub-isolation-test-broker.service")
unit.write_text(service)
subprocess.run(["systemctl", "daemon-reload"], check=True)
subprocess.run(["systemctl", "start", unit.name], check=True)
print("Provisioned isolated native integration fixtures")
