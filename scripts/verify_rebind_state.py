# -*- coding: utf-8 -*-
"""验证 PS 注册被拒原因: PM 侧 user3 哈希是否与 PS local_users 明文码匹配"""
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
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, _ = sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT auth_code FROM local_users;\"")
    ps_code = out.strip()
    print("PS local_users 明文码: %s...%s" % (ps_code[:12], ps_code[-6:]))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    out, _ = run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT auth_code, auth_code_salt, updated_at FROM users WHERE username='user3';\"")
    print("PM user3 记录: %s" % out.strip())
    parts = out.strip().split("|")
    pm_hash, pm_salt = parts[0], parts[1]
    calc = hashlib.sha256((pm_salt + ps_code).encode()).hexdigest()
    print("\nPM 库中哈希: %s" % pm_hash)
    print("PS 码计算哈希: %s" % calc)
    print("=> %s" % ("匹配(不是重置问题,需另查)" if calc == pm_hash else "不匹配 => 用户确已重置授权码, PS 等待重绑属预期"))
    out, _ = run(ssh, "grep -E 'auth code reset|Kicked user3' /root/seeinp/logs/seeinpm.log | tail -n 5")
    print("\nPM 重置日志: %s" % out.strip())
finally:
    ssh.close()
