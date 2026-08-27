# -*- coding: utf-8 -*-
"""部署 0827-v3 第二步: seeinps 二进制 + web 前端升级(启动命令独立执行防通道挂起)。"""
import os

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"
LOCAL_PS_WEB = r"d:\ide\seeinp\web-ps\dist"


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
    show("停止 seeinps", *run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep -v grep | grep seeinps || echo '已停止'", sudo=True))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    print("二进制上传完成")
    run(ssh, "rm -rf /tmp/webdist && mkdir -p /tmp/webdist/assets", sudo=True)
    count = 0
    for root, _dirs, files in os.walk(LOCAL_PS_WEB):
        for fn in files:
            local_path = os.path.join(root, fn)
            rel = os.path.relpath(local_path, LOCAL_PS_WEB).replace("\\", "/")
            sftp.put(local_path, f"/tmp/webdist/{rel}")
            count += 1
    sftp.close()
    print(f"前端上传完成: {count} 个文件")
    show("替换二进制", *run(ssh, "mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps", sudo=True))
    show("安装 web 前端", *run(ssh, "rm -rf /root/seeinp/web && cp -r /tmp/webdist /root/seeinp/web && rm -rf /tmp/webdist && ls /root/seeinp/web/", sudo=True))
    show("启动 seeinps", *run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 & sleep 5; ps aux | grep -v grep | grep seeinps || echo '启动失败'", sudo=True))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("日志尾部", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log", sudo=True))
finally:
    ssh.close()

print("\n========== seeinpm 侧验证 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("seeinpm 日志尾部", *run(ssh, "tail -n 12 /root/seeinp/logs/seeinpm.log"))
    show("公网端口监听", *run(ssh, "ss -tlnp | grep seeinpm"))
finally:
    ssh.close()

print("\nseeinps 部署完成")
