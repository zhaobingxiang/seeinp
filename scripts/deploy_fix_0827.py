# -*- coding: utf-8 -*-
"""部署: seeinpm 重置数据库并重启; 内网服务器上传新 seeinps 并启动。"""
import time

import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"


def run(ssh, cmd, sudo_pwd=None, timeout=20):
    if sudo_pwd:
        stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
        stdin.write((sudo_pwd + "\n").encode())
        stdin.channel.shutdown_write()
    else:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1200])


print("========== 1. seeinpm 服务器: 清理 + 重置数据库 + 重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("停掉残留 seeinps", *run(ssh, "pkill -f './seeinps' ; sleep 1; ps aux | grep -v grep | grep -c seeinps || echo '0'"))
    show("停掉 seeinpm", *run(ssh, "pkill -f './seeinpm' ; sleep 1"))
    show("删除数据库(保留证书)", *run(ssh, "rm -f /root/seeinp/data/seeinpm.db /root/seeinp/data/seeinpm.db-shm /root/seeinp/data/seeinpm.db-wal; ls /root/seeinp/data/"))
    show("重启 seeinpm", *run(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 & sleep 3; ps aux | grep -v grep | grep seeinpm"))
    show("端口监听", *run(ssh, "ss -tlnp | grep -E ':(999|9998|65443)\\b'"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("admin 应为未初始化(期望非3001)", *run(ssh, "curl -s -m 5 -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:9998/api/v1/auth/init"))
finally:
    ssh.close()

print("\n========== 2. 内网服务器: 上传新 seeinps 并启动 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止旧进程", *run(ssh, "pkill -f './seeinps' ; sleep 1; ps aux | grep -v grep | grep seeinps || echo '已停止'", sudo_pwd=PS_PWD))
    print("\n--- 上传二进制 ---")
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    sftp.close()
    print("上传完成")
    show("替换二进制", *run(ssh, "sudo -S mv /tmp/seeinps.new /root/seeinp/seeinps && sudo -S chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps", sudo_pwd=PS_PWD))
    show("启动 seeinps", *run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 & sleep 4; ps aux | grep -v grep | grep seeinps", sudo_pwd=PS_PWD))
    show("端口监听", *run(ssh, "ss -tlnp | grep ':65443'", sudo_pwd=PS_PWD))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("连接状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/status"))
    show("日志尾部", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log", sudo_pwd=PS_PWD))
finally:
    ssh.close()
