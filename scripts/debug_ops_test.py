# -*- coding: utf-8 -*-
"""查 seeinps 数据库代理配置 + 完整运维代理链路测试。"""
import json
import time

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
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


print("========== 1. 查看代理配置(数据库) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("local_proxies 表", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, local_addr, local_port, public_port, proxy_username, acl, status FROM local_proxies;' 2>/dev/null || echo '无sqlite3'"))
finally:
    ssh.close()

print("\n========== 2. 从内网服务器 telnet 公网 20003 (模拟用户路径) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("内网 telnet 公网 20003", *run(ssh, "timeout 5 bash -c 'echo > /dev/tcp/170.106.109.105/20003 && echo TCP通' 2>&1 || echo 'TCP不通'"))
    show("内网 curl 代理无认证(期望407)", *run(ssh, "curl -s -m 8 -i -x http://170.106.109.105:20003 http://www.baidu.com/ 2>&1 | head -6"))
finally:
    ssh.close()

print("\n========== 3. seeinpm 侧查看 20003 最近连接日志 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("20003 相关日志", *run(ssh, "grep '20003' /root/seeinp/logs/seeinpm.log | tail -n 10"))
finally:
    ssh.close()
