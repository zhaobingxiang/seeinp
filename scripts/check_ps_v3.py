# -*- coding: utf-8 -*-
"""检查 PS 服务器 seeinps 运行状态。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程状态", *sudo_run(ssh, "ps aux | grep -v grep | grep seeinps || echo '未运行'"))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status || echo '无响应'"))
    show("web 首页", *run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' http://127.0.0.1:65443/"))
    show("日志尾部", *sudo_run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

print("\n========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("seeinpm 日志尾部", *run(ssh, "tail -n 8 /root/seeinp/logs/seeinpm.log"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
finally:
    ssh.close()
