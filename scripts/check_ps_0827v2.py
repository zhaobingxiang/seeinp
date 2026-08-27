# -*- coding: utf-8 -*-
"""检查内网 seeinps 运行状态。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def run(ssh, cmd, sudo_pwd=None, timeout=20):
    if sudo_pwd:
        stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
        stdin.write((sudo_pwd + "\n").encode())
        stdin.channel.shutdown_write()
    else:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep -v grep | grep seeinps || echo '<< 未运行 >>'"))
    show("端口 65443", *run(ssh, "ss -tln | grep ':65443' || echo '<< 未监听 >>'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '<< 无响应 >>'"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("日志尾部", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log", sudo_pwd=PS_PWD))
    show("数据目录", *run(ssh, "ls -la /root/seeinp/data/ 2>/dev/null", sudo_pwd=PS_PWD))
finally:
    ssh.close()
