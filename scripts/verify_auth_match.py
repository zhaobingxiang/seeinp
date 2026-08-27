# -*- coding: utf-8 -*-
"""验证 PS conf 的明文授权码是否与 PM DB 中用户的 hash 匹配"""
import hashlib

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


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    out, _ = run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, auth_code, auth_code_salt FROM users;\"")
    print("DB row:", out.strip())
    username, db_hash, salt = out.strip().split("|")
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, _ = sudo_run(ssh, "grep auth_code /root/seeinp/conf/seeinps.toml")
    ps_code = out.strip().split('"')[1]
finally:
    ssh.close()

calc = hashlib.sha256((salt + ps_code).encode()).hexdigest()
print("\nPS 明文码:", ps_code[:24] + "...")
print("DB hash  :", db_hash)
print("计算hash :", calc)
print("\n结论:", "匹配 ✅ 重启 PS 安全" if calc == db_hash else "不匹配 ❌ 重启 PS 将无法注册")
