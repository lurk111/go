"""Generate isolated SSH identities inside Docker volumes, preserving keys on restart."""

import os
from pathlib import Path
import secrets

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


def keypair(path):
    if not path.exists():
        key = Ed25519PrivateKey.generate()
        path.write_bytes(
            key.private_bytes(
                serialization.Encoding.PEM, serialization.PrivateFormat.OpenSSH, serialization.NoEncryption()
            )
        )
    else:
        key = serialization.load_ssh_private_key(path.read_bytes(), None)
    return key.public_key().public_bytes(serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH)


client, target = Path("/ssh-client"), Path("/ssh-target")
client.mkdir(exist_ok=True)
target.mkdir(exist_ok=True)
public = keypair(client / "id_ed25519")
host = keypair(target / "host_key")
command = b"/opt/bid-mon-helper"
(target / "authorized_keys").write_bytes(b'restrict,command="' + command + b'" ' + public + b"\n")
(client / "known_hosts").write_bytes(b"ssh-target " + host + b"\n")
binding = client / "binding.key"
if not binding.exists():
    binding.write_bytes(secrets.token_bytes(32))
(target / "binding.key").write_bytes(binding.read_bytes())
for path in client.iterdir():
    os.chown(path, 10001, 10001)
    path.chmod(0o600)
for path in target.iterdir():
    os.chown(path, 0 if path.name == "host_key" else 10001, 10001)
    path.chmod(0o600)
root = Path("/evidence")
(root / "tasks").mkdir(parents=True, exist_ok=True)
for path in (root, root / "tasks"):
    os.chown(path, 10001, 10001)
    path.chmod(0o755)
print("SSH identities and evidence volume initialized; secret values not printed.")
