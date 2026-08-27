# -*- coding: utf-8 -*-
"""紧急恢复: 删除 e2e-check 嫌疑数据 -> 重启 seeinps -> 验证。"""
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
    print((out or err).strip()[:2000])


print("========== 1. 查看崩溃现场(日志最后30行) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("日志最后30行(找panic)", *sudo_run(ssh, "tail -n 30 /root/seeinp/logs/seeinps.log"))
    show("删除 e2e-check", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"DELETE FROM local_proxies WHERE proxy_id='e2e-check';\" && sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, public_port FROM local_proxies;'"))
    show("启动 seeinps", *sudo_run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown; echo relaunched"))
finally:
    ssh.close()

time.sleep(6)

print("\n========== 2. 验证恢复 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '仍未运行!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("启动日志", *sudo_run(ssh, "grep -v OPS /root/seeinp/logs/seeinps.log | tail -n 12"))
finally:
    ssh.close()

print("\n========== 3. seeinpm 侧确认 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("端口监听", *run(ssh, "ss -tlnp | grep -E ':(20001|20002|20003)\\b'"))
finally:
    ssh.close()
