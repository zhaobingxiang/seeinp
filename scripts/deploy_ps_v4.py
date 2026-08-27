# -*- coding: utf-8 -*-
"""部署 seeinps ops-v2: 缓存头修复。sudo 用 sh -c 包裹完整链，启动后独立验证。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"


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
    print((out or err).strip()[:1200])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep -v grep | grep seeinps || echo stopped"))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    sftp.close()
    print("二进制上传完成")
    show("替换并授权", *sudo_run(ssh, "mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps"))
    show("启动", *sudo_run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown; echo launched"))
    time.sleep(5)
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程状态", *sudo_run(ssh, "ps aux | grep -v grep | grep seeinps || echo '未运行'"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("index.html 缓存头(期望 no-cache)", *run(ssh, "curl -s -m 5 -I http://127.0.0.1:65443/ | grep -i cache-control"))
    show("assets 缓存头(期望 immutable)", *run(ssh, "curl -s -m 5 -I http://127.0.0.1:65443/assets/index-Bacf4wvv.js | grep -i cache-control"))
    show("日志尾部", *sudo_run(ssh, "tail -n 10 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

print("\n========== seeinpm 侧重连验证 ==========")
time.sleep(2)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("seeinpm 日志尾部", *run(ssh, "tail -n 6 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
