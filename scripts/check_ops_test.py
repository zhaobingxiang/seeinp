# -*- coding: utf-8 -*-
"""检查运维代理 test(20003) 的状态。"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


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


print("========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("端口监听", *run(ssh, "ss -tlnp | grep seeinpm"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("日志尾部", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinpm.log"))
    show("本地 20003 直连测试", *run(ssh, "curl -s -m 5 -i http://127.0.0.1:20003/ | head -8 || echo '连接失败'"))
finally:
    ssh.close()

print("\n========== seeinps 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("seeinps 日志尾部", *sudo_run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log"))
    show("代理列表(需token,先看数据库)", *sudo_run(ssh, "ls -la /root/seeinp/data/"))
finally:
    ssh.close()
