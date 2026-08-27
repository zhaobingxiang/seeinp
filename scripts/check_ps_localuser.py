# -*- coding: utf-8 -*-
"""排查 PS 注册凭证来源: local_user 表 vs conf"""
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
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, err = sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db '.tables'")
    print("PS库表:", out.strip() or err.strip())
    out, err = sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db '.schema local_user' 2>/dev/null; sqlite3 /root/seeinp/data/seeinps.db \"SELECT * FROM local_user;\" 2>/dev/null")
    print("local_user:", (out.strip() or "(空/不存在)")[:500])
    out, err = sudo_run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log")
    print("\nPS日志尾部:\n", out.strip()[-1200:])
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    out, _ = run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, auth_code, auth_code_salt, status FROM users;\"")
    print("\nPM users:", out.strip())
finally:
    ssh.close()
