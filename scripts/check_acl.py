# -*- coding: utf-8 -*-
"""查看 http 代理当前完整配置(重点: ACL 是否被改宽)"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=25):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:1500])
    print()


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("http代理完整配置", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, type, proxy_username, acl, local_addr, local_port, public_port FROM local_proxies WHERE proxy_id='http';\""))
    show("全部代理", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, public_port FROM local_proxies;'"))
finally:
    ssh.close()
