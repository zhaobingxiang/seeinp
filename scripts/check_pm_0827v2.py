# -*- coding: utf-8 -*-
"""检查 pm 服务器 seeinpm 当前运行状态。"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def run(ssh, cmd, timeout=20):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:1200])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep -v grep | grep seeinpm || echo '<< 未运行 >>'"))
    show("端口", *run(ssh, "ss -tlnp | grep -E ':(999|9998)\\b' || echo '<< 未监听 >>'"))
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '<< 无响应 >>'"))
    show("日志", *run(ssh, "tail -n 10 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
