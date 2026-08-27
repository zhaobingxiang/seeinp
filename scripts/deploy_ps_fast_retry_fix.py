# -*- coding: utf-8 -*-
"""修正部署顺序: 停进程 → 拷贝(此时无 Text file busy) → 启动 → 验证"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=30):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def sudo_fire(ssh, cmd):
    chan = ssh.get_transport().open_session()
    chan.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'")
    time.sleep(0.5)
    chan.sendall((PS_PWD + "\n").encode())
    time.sleep(2)
    chan.close()


def show(name, out, err=""):
    text = (out or err).strip()
    print(f"--- {name} ---")
    print(text[:1000] if text else "(empty)")
    print()


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停进程", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep seeinps | grep -v grep || echo '已停止'"))
    show("拷贝新二进制", *sudo_run(ssh, "cp /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && echo COPY_OK"))
    show("md5 一致性", *sudo_run(ssh, "md5sum /tmp/seeinps.new /root/seeinp/seeinps"))
    sudo_fire(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown")
    print("PS 已启动")
finally:
    ssh.close()

time.sleep(10)

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("PS 日志(期望 Registered OK + 3 ALLOC)", *sudo_run(ssh, "grep -E 'Registered|ALLOC|error' /root/seeinp/logs/seeinps.log | tail -n 6"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM 健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("PM 监听", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
    show("TCP 20001 连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20001' && echo TCP_OK || echo TCP_FAIL"))
    show("ops 20003 存活(期望407)", *run(ssh, "curl -s -m 10 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
finally:
    ssh.close()
