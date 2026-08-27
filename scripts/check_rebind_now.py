# -*- coding: utf-8 -*-
"""检查当前实时状态: PS 是否已注册 / user3 是否在线 / 双端日志"""
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
    print("== PS 侧 ==")
    out, _ = sudo_run(ssh, "ps aux | grep seeinps | grep -v grep | awk '{print $2, $11, $12}'")
    print("进程: %s" % out.strip())
    out, _ = sudo_run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log")
    print("PS 日志尾部:\n%s" % out.strip())
    out, _ = sudo_run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status")
    print("B端 auth/status: %s" % out.strip())
    out, _ = sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT username, seeinpm_user, substr(auth_code,1,14), registered, must_change, updated_at FROM local_users;\"")
    print("local_users: %s" % out.strip())
finally:
    ssh.close()

print()
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    print("== PM 侧 ==")
    out, _ = run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, status, online_session, datetime(updated_at,'unixepoch','+8 hours') FROM users;\"")
    print("users: %s" % out.strip())
    out, _ = run(ssh, "tail -n 15 /root/seeinp/logs/seeinpm.log")
    print("PM 日志尾部:\n%s" % out.strip())
    out, _ = run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}' | tr '\\n' ' '")
    print("PM 监听: %s" % out.strip())
finally:
    ssh.close()
