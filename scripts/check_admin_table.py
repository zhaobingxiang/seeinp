# -*- coding: utf-8 -*-
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    for cmd in [
        "sqlite3 /root/seeinp/data/seeinpm.db '.tables'",
        "sqlite3 /root/seeinp/data/seeinpm.db '.schema admin_users'",
        "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, substr(password_hash,1,7) FROM admin_users;\"",
    ]:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=15)
        stdout.channel.recv_exit_status()
        out = stdout.read().decode("utf-8", "replace").strip()
        err = stderr.read().decode("utf-8", "replace").strip()
        print("$", cmd)
        print((out or err) or "(empty)")
        print()
finally:
    ssh.close()
