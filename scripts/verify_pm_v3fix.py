# -*- coding: utf-8 -*-
"""验证 seeinpm v3 启动状态。"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinpm | grep -v grep || echo '未运行!'"))
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '无响应'"))
    show("日志尾部", *run(ssh, "tail -n 8 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
