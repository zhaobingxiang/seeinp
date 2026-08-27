# -*- coding: utf-8 -*-
"""查 seeinpm port_allocations 现状。"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:2000])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("port_allocations 全部记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT id, port, user_id, proxy_id, proxy_type, status, datetime(allocated_at,\"unixepoch\"), datetime(released_at,\"unixepoch\") FROM port_allocations ORDER BY id;'"))
    show("users 表在线状态", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT username, online_session, datetime(online_since,\"unixepoch\") FROM users;'"))
    show("CLEANUP 日志", *run(ssh, "grep CLEANUP /root/seeinp/logs/seeinpm.log | tail -5"))
finally:
    ssh.close()
