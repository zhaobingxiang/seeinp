# -*- coding: utf-8 -*-
"""内网服务器: 上传新 seeinps 二进制并启动(后台启动用 < /dev/null 防通道挂起)。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"


def run(ssh, cmd, sudo=False, timeout=30):
    full = ("sudo -S " + cmd) if sudo else cmd
    stdin, stdout, stderr = ssh.exec_command(full, timeout=timeout)
    if sudo:
        stdin.write((PS_PWD + "\n").encode())
        stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1200])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止旧进程", *run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep -v grep | grep seeinps || echo '已停止'", sudo=True))

    print("\n--- 上传二进制 ---")
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    sftp.close()
    print("上传完成")

    show("替换二进制", *run(ssh, "mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps && ls -la /root/seeinp/seeinps", sudo=True))

    # 后台启动: stdin 重定向到 /dev/null, 不等待通道
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c 'cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null &'", timeout=10)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    time.sleep(5)

    show("进程状态", *run(ssh, "ps aux | grep -v grep | grep seeinps || echo '<< 未运行 >>'", sudo=True))
    show("端口监听", *run(ssh, "ss -tlnp | grep ':65443' || echo '<< 65443 未监听 >>'", sudo=True))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '<< 无响应 >>'", sudo=True))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status", sudo=True))
    show("连接状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/status", sudo=True))
    show("日志尾部", *run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log", sudo=True))
finally:
    ssh.close()
