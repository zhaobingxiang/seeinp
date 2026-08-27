# -*- coding: utf-8 -*-
"""验证 seeinps 恢复。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def sudo_run(ssh, cmd, timeout=20):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("非OPS日志(启动段)", *sudo_run(ssh, "grep -v OPS /root/seeinp/logs/seeinps.log | tail -n 10"))
finally:
    ssh.close()

print("\n========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("20003 监听", *run(ssh, "ss -tlnp | grep ':20003'"))
finally:
    ssh.close()
