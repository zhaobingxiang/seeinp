# -*- coding: utf-8 -*-
"""确认: 代理密码是否被用户通过web UI修改(对比哈希+updated_at) + 我刚才测试的日志记录"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


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
    print((out or err).strip()[:1500])
    print()


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("代理密码哈希前缀+更新时间", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT substr(proxy_password,1,25), datetime(updated_at,'unixepoch','+8 hours'), datetime(created_at,'unixepoch','+8 hours') FROM local_proxies WHERE proxy_id='http';\""))
    show("我设置的哈希前缀(对照)", "expected: $2a$10$dY1dYgjF/.JOYLTvkz5Sie")
    show("当前北京时间", time.strftime("%Y-%m-%d %H:%M:%S"))
    show("最近OPS日志(我刚才的3次407测试)", *sudo_run(ssh, "grep '\\[OPS\\]' /root/seeinp/logs/seeinps.log | tail -n 5"))
finally:
    ssh.close()
