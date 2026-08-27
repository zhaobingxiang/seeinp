# -*- coding: utf-8 -*-
"""检查用户代理工具测试期间的两侧日志: 请求是否到达 seeinps, 认证结果如何"""
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


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:2500])
    print()


print("========== seeinps 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("服务器时间", *run(ssh, "date '+%F %T'"))
    show("OPS访问日志(最近20条)", *sudo_run(ssh, "grep '\\[OPS\\]' /root/seeinp/logs/seeinps.log | tail -n 20"))
    show("PS日志尾部(非OPS, 最近10条)", *sudo_run(ssh, "grep -v '\\[OPS\\]' /root/seeinp/logs/seeinps.log | tail -n 10"))
finally:
    ssh.close()

print("========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("服务器时间", *run(ssh, "date '+%F %T'"))
    show("PM日志尾部(最近25条)", *run(ssh, "tail -n 25 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
