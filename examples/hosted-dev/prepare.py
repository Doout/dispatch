#!/usr/bin/env python3
"""Prepare a private Docker deployment directory without starting services."""

import argparse
import ipaddress
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import sys


def owner_id(value):
    result = int(value)
    if not 0 <= result < 2**32 - 1:
        raise ValueError("owner IDs must be nonnegative integers below 4294967295")
    return result


def read_token(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise ValueError("Cloudflare token must be a private regular file")
        raw = source.read(4098)
        if len(raw) > 4097:
            raise ValueError("Cloudflare token file is too large")
    try:
        token = raw.decode("ascii").strip()
    except UnicodeDecodeError:
        raise ValueError("Cloudflare token must contain printable ASCII") from None
    if not token or len(token) > 4096 or any(ord(c) <= 32 or ord(c) >= 127 for c in token):
        raise ValueError("Cloudflare token is empty or invalid")
    return token


def write_file(path, content, uid, gid):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as target:
        os.fchown(target.fileno(), uid, gid)
        target.write(content)


def prepare(args):
    if os.geteuid() != 0:
        raise ValueError("run with sudo to assign container file ownership")
    state = Path(args.state_dir)
    if not state.is_absolute() or state.name in ("", ".", ".."):
        raise ValueError("state directory must be a new absolute path")
    if any(c in str(state) for c in "'\r\n\x00:"):
        raise ValueError("state directory cannot contain quotes, newlines or colons")
    root = args.root_domain.lower().rstrip(".")
    label = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
    if len(root) > 253 or not re.fullmatch(label + r"(?:\." + label + r")+", root):
        raise ValueError("root domain must be a DNS hostname")
    try:
        ipaddress.ip_address(root)
    except ValueError:
        pass
    else:
        raise ValueError("root domain cannot be an IP address")
    address = str(ipaddress.ip_address(args.console_address))
    proxy = ipaddress.ip_network(args.ingress_proxy_cidr, strict=True)
    if proxy.prefixlen != proxy.max_prefixlen:
        raise ValueError("ingress proxy CIDR must identify one IP with /32 or /128")
    zone = args.cloudflare_zone_id.lower()
    if not re.fullmatch(r"[a-f0-9]{32}", zone):
        raise ValueError("Cloudflare zone ID must contain 32 hexadecimal characters")
    email = args.admin_email.strip().lower()
    if len(email) > 254 or email.count("@") != 1 or email.startswith("@") or email.endswith("@") or any(c.isspace() or ord(c) < 32 for c in email) or "'" in email:
        raise ValueError("administrator email is invalid")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}", args.image):
        raise ValueError("image must be a Docker image reference")
    uid, gid = owner_id(args.owner_uid), owner_id(args.owner_gid)
    token = read_token(args.cloudflare_token_file)
    source = Path(__file__).resolve().parent
    templates = {name: (source / name).read_text() for name in ("compose.yml", "nginx.conf.template", "init-postgres.sh")}
    os.umask(0o077)
    # mkdir is exclusive. An existing installation must never lose credentials.
    state.mkdir(mode=0o700)
    try:
        for name, owner in (("data", 65532), ("platform-secrets", 65532), ("database-secrets", 999)):
            path = state / name
            path.mkdir(mode=0o700)
            os.chown(path, owner, owner)
        admin_password, catalog_password, provisioner_password, database_password = (secrets.token_urlsafe(32) for _ in range(4))
        platform = {
            "catalog-url": f"postgres://dispatch_catalog:{catalog_password}@postgres:5432/dispatch_catalog?sslmode=disable",
            "provisioner-url": f"postgres://dispatch_provisioner:{provisioner_password}@postgres:5432/dispatch_maintenance?sslmode=disable",
            "cloudflare-token": token,
            "initial-admin-password": admin_password,
        }
        database = {"database-admin-password": database_password, "catalog-password": catalog_password, "provisioner-password": provisioner_password}
        for directory, entries, owner in (("platform-secrets", platform, 65532), ("database-secrets", database, 999)):
            for name, value in entries.items():
                write_file(state / directory / name, value + "\n", owner, owner)
        env = {
            "DISPATCH_HOSTED_STATE_DIR": str(state),
            "DISPATCH_HOSTED_ROOT_DOMAIN": root,
            "DISPATCH_HOSTED_ADMIN_EMAIL": email,
            "DISPATCH_HOSTED_CONSOLE_ADDRESSES": address,
            "DISPATCH_HOSTED_CLOUDFLARE_ZONE_ID": zone,
            "DISPATCH_INGRESS_PROXY_CIDR": str(proxy),
            "DISPATCH_PLATFORM_IMAGE": args.image,
            "DISPATCH_HOSTED_ROUTER_RULE": f"Host(`{root}`) || HostRegexp(`^[a-z0-9]([a-z0-9-]{{0,61}}[a-z0-9])?\\.{re.escape(root)}$`)",
        }
        write_file(state / ".env", "".join(f"{name}='{value}'\n" for name, value in env.items()), uid, gid)
        write_file(state / "initial-admin.txt", f"URL: https://{root}\nEmail: {email}\nPassword: {admin_password}\n", uid, gid)
        for name, content in templates.items():
            write_file(state / name, content, uid, gid)
            # Container users need to read these bind-mounted configuration files.
            os.chmod(state / name, 0o755 if name == "init-postgres.sh" else 0o644)
        os.chown(state, uid, gid)
    except BaseException:
        shutil.rmtree(state)
        raise
    print(f"Prepared {state}. Initial administrator credentials are in initial-admin.txt.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-dir", required=True)
    parser.add_argument("--root-domain", required=True)
    parser.add_argument("--console-address", required=True)
    parser.add_argument("--cloudflare-zone-id", required=True)
    parser.add_argument("--cloudflare-token-file", required=True)
    parser.add_argument("--ingress-proxy-cidr", required=True)
    parser.add_argument("--admin-email", required=True)
    parser.add_argument("--owner-uid", default=os.environ.get("SUDO_UID", os.getuid()))
    parser.add_argument("--owner-gid", default=os.environ.get("SUDO_GID", os.getgid()))
    parser.add_argument("--image", default="ghcr.io/doout/dispatch-platform:main")
    try:
        prepare(parser.parse_args())
    except (OSError, ValueError) as error:
        print(f"Cannot prepare deployment: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
