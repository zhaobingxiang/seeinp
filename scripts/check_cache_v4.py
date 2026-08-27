# -*- coding: utf-8 -*-
"""验证缓存头是否生效。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:800])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("index.html 响应头", *run(ssh, "curl -s -m 5 -I http://127.0.0.1:65443/"))
    show("index.html 缓存头(期望 no-cache)", *run(ssh, "curl -s -m 5 -I http://127.0.0.1:65443/ | grep -i cache-control"))
    show("assets JS 缓存头(期望 immutable)", *run(ssh, "curl -s -m 5 -I http://127.0.0.1:65443/assets/index-Bacf4wvv.js | grep -i cache-control"))
finally:
    ssh.close()
