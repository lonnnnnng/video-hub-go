#!/usr/bin/env python3
"""从公开 GitHub Release 更新本机 systemd 服务。"""

import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tempfile
import time
import urllib.request
from urllib.parse import urlsplit


REPO = "lonnnnnng/video-hub-go"
TRUSTED_HOSTS = {"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"}
MAX_BINARY = 128 * 1024 * 1024
SYSTEMCTL = ["systemctl", "--user"] if os.environ.get("MOVIEHUB_SYSTEMD_SCOPE", "user") == "user" else ["systemctl"]
SERVICE = os.environ.get("MOVIEHUB_SERVICE_NAME", "video-hub-go")
BINARY = Path(os.environ.get("MOVIEHUB_BINARY", str(Path(__file__).resolve().parent.parent / "bin/moviehub")))


def read_url(url, limit, release_asset=False):
    req = urllib.request.Request(url, headers={"User-Agent": "video-hub-go-updater", "Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req, timeout=90) as response:
        target = urlsplit(response.geturl())
        if target.scheme != "https" or (release_asset and target.hostname not in TRUSTED_HOSTS):
            raise RuntimeError("Release 下载重定向到非可信地址")
        data = response.read(limit + 1)
        if len(data) > limit:
            raise RuntimeError("Release 文件超出大小限制")
        return data


def latest_release():
    release = json.loads(read_url(f"https://api.github.com/repos/{REPO}/releases/latest", 1024 * 1024))
    if release.get("draft") or release.get("prerelease") or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", release.get("tag_name", "")):
        raise RuntimeError("GitHub 最新 Release 不是正式语义版本")
    return release


def asset(release, name):
    matches = [entry["browser_download_url"] for entry in release["assets"] if entry["name"] == name]
    if len(matches) != 1 or not matches[0].startswith(f"https://github.com/{REPO}/releases/download/"):
        raise RuntimeError(f"Release 缺少可信资产 {name}")
    return matches[0]


def health_url():
    explicit = os.environ.get("MOVIEHUB_HEALTH_URL")
    if explicit:
        url = explicit
    else:
        # long: 从实际运行的 unit 读取监听地址，避免升级器误查默认回环端口而把局域网服务判为故障。
        result = subprocess.run([*SYSTEMCTL, "show", "-p", "ExecStart", "--value", f"{SERVICE}.service"], check=True, capture_output=True, text=True)
        match = re.search(r"(?:^|\s)--listen(?:=|\s+)([^\s;]+)", result.stdout)
        if not match:
            raise RuntimeError("无法从服务配置读取监听地址，请设置 MOVIEHUB_HEALTH_URL")
        listen = match.group(1)
        if listen.startswith("0.0.0.0:"):
            listen = "127.0.0.1:" + listen.rsplit(":", 1)[1]
        elif listen.startswith("[::]:"):
            listen = "[::1]:" + listen.rsplit(":", 1)[1]
        url = f"http://{listen}/healthz"
    target = urlsplit(url)
    try:
        ipaddress.ip_address(target.hostname or "")
    except ValueError:
        if target.hostname != "localhost":
            raise RuntimeError("健康检查必须使用本机 IP 或 localhost") from None
    if target.scheme != "http" or target.path != "/healthz" or target.username or target.password or target.query or target.fragment or not target.port:
        raise RuntimeError("健康检查地址必须是本机 HTTP /healthz")
    return url


def current_version(url):
    try:
        with urllib.request.urlopen(url, timeout=5) as response:
            health = json.load(response)
        if health.get("status") != "ok":
            raise RuntimeError("健康接口返回异常")
        return health.get("version", "unknown")
    except (OSError, ValueError):
        raise RuntimeError("无法读取运行中服务的健康接口") from None


def systemctl(action):
    subprocess.run([*SYSTEMCTL, action, f"{SERVICE}.service"], check=True)


def wait_version(version, url):
    for _ in range(30):
        try:
            if current_version(url) == version:
                return True
        except RuntimeError:
            pass
        time.sleep(1)
    return False


def apply_update(release, name, url):
    checksum = read_url(asset(release, "SHA256SUMS"), 1024 * 1024, True).decode("ascii")
    expected = None
    for line in checksum.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1] == name and re.fullmatch(r"[0-9a-fA-F]{64}", fields[0]):
            expected = fields[0].lower()
    if expected is None:
        raise RuntimeError("SHA256SUMS 中没有对应程序")
    binary = read_url(asset(release, name), MAX_BINARY, True)
    if hashlib.sha256(binary).hexdigest() != expected:
        raise RuntimeError("下载程序的 SHA256 校验失败")
    if not BINARY.is_file() or BINARY.is_symlink():
        raise RuntimeError("当前程序路径不是普通文件")
    # long: 先把已校验的新程序写入同目录临时文件，再原子替换；旧程序留作启动失败时的回滚依据。
    with tempfile.NamedTemporaryFile(prefix=".moviehub-update-", dir=BINARY.parent, delete=False) as stage:
        stage_path = Path(stage.name)
        try:
            stage.write(binary)
            stage.flush()
            os.fsync(stage.fileno())
        except Exception:
            stage_path.unlink(missing_ok=True)
            raise
    os.chmod(stage_path, 0o755)
    backup = BINARY.with_name(BINARY.name + f".previous-{int(time.time())}")
    if backup.exists():
        stage_path.unlink(missing_ok=True)
        raise RuntimeError(f"已有旧程序备份：{backup}，请先检查")
    replaced = False
    try:
        os.link(BINARY, backup)
        os.replace(stage_path, BINARY)
        replaced = True
        systemctl("restart")
        if not wait_version(release["tag_name"], url):
            raise RuntimeError("新服务未在 30 秒内报告目标版本")
    except Exception:
        stage_path.unlink(missing_ok=True)
        if replaced:
            os.replace(backup, BINARY)
            systemctl("restart")
        else:
            backup.unlink(missing_ok=True)
        raise
    print(f"升级完成：{release['tag_name']}；旧程序备份：{backup}")


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("health", "check-update", "update"):
        raise RuntimeError("用法：moviehub-update.py {health|check-update|update}")
    if sys.argv[1] == "health":
        url = health_url()
        print(f"服务健康：{current_version(url)}")
        return
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise RuntimeError("仅支持 Linux amd64/arm64")
    if not re.fullmatch(r"[a-z][a-z0-9-]*", SERVICE):
        raise RuntimeError("服务名格式无效")
    url = health_url()
    current = current_version(url)
    release = latest_release()
    latest = release["tag_name"]
    name = f"moviehub-linux-{arch}"
    asset(release, name)
    asset(release, "SHA256SUMS")
    print(f"当前版本：{current}；最新版本：{latest}")
    parse = lambda value: tuple(map(int, value[1:].split("."))) if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", value) else None
    current_number = parse(current)
    latest_number = parse(latest)
    if sys.argv[1] == "check-update" or (current_number is not None and current_number >= latest_number):
        return
    apply_update(release, name, url)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"在线升级失败：{exc}", file=sys.stderr)
        sys.exit(1)
