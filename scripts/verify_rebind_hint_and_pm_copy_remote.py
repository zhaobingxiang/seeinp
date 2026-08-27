# -*- coding: utf-8 -*-
"""在测试服务器本机回环验证本轮修复（避免公网映射差异）:
1) web-ps 新前端包含错误授权码提示文案
2) web-pm 新前端包含复制兼容文案
3) B端 rebind 接口存在（未登录 401）
"""
import json
import urllib.request

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=25):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


# --- PS 本地回环检测 ---
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, _ = sudo_run(ssh, "grep -R \"绑定失败，请确认授权码是否正确\" /root/seeinp/web/assets/ | head -n 1 || true")
    print("[check] PS frontend hint present:", bool((out or '').strip()))
    out, _ = sudo_run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:65443/api/v1/auth/rebind -H 'Content-Type: application/json' -d '{\"authCode\":\"wrong\"}'")
    print("[check] PS rebind endpoint status:", out.strip())
finally:
    ssh.close()

# --- PM 本地回环检测 ---
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    out, _ = run(ssh, "grep -R \"自动复制失败，请手动复制授权码\" /root/seeinp/web-pm/dist/assets/ | head -n 1 || true")
    print("[check] PM frontend copy fallback text:", bool((out or '').strip()))
finally:
    ssh.close()
