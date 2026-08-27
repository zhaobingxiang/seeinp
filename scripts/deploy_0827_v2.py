# -*- coding: utf-8 -*-
"""部署 0827-v2: 流头格式变更(stype+len+proxyId)，pm/ps 必须同时升级。
顺序: 停 ps -> 升级 pm(保留数据库) -> 部署 ps 二进制+web -> 启动 -> 验证。
"""
import os
import time

import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
LOCAL_PM_BIN = r"d:\ide\seeinp\seeinpm-linux-amd64"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"
LOCAL_PS_WEB = r"d:\ide\seeinp\web-ps\dist"


def run(ssh, cmd, sudo_pwd=None, timeout=30):
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
    print((out or err).strip()[:1500])


print("========== 1. 先停内网 seeinps(旧协议不兼容) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止旧 seeinps", *run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep -v grep | grep seeinps || echo '已停止'", sudo_pwd=PS_PWD))
finally:
    ssh.close()

print("\n========== 2. seeinpm 服务器: 升级二进制(保留数据库) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("停止 seeinpm", *run(ssh, "pkill -f './seeinpm'; sleep 1"))
    print("\n--- 上传 seeinpm 二进制 ---")
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PM_BIN, "/tmp/seeinpm.new")
    sftp.close()
    print("上传完成")
    show("替换二进制", *run(ssh, "mv /tmp/seeinpm.new /root/seeinp/seeinpm && chmod +x /root/seeinp/seeinpm && md5sum /root/seeinp/seeinpm"))
    show("重启 seeinpm", *run(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 & sleep 3; ps aux | grep -v grep | grep seeinpm"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("日志尾部", *run(ssh, "tail -n 5 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()

print("\n========== 3. 内网服务器: 部署 seeinps 二进制 + web ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    print("\n--- 上传 seeinps 二进制 ---")
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    sftp.close()
    print("上传完成")
    show("替换二进制", *run(ssh, "mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps", sudo_pwd=PS_PWD))

    print("\n--- 上传 web-ps 前端 ---")
    sftp = ssh.open_sftp()
    run(ssh, "rm -rf /root/seeinp/web && mkdir -p /root/seeinp/web/assets", sudo_pwd=PS_PWD)
    count = 0
    for root, _dirs, files in os.walk(LOCAL_PS_WEB):
        for fn in files:
            local_path = os.path.join(root, fn)
            rel = os.path.relpath(local_path, LOCAL_PS_WEB).replace("\\", "/")
            remote_path = f"/tmp/webdist/{rel}"
            try:
                sftp.put(local_path, remote_path)
            except FileNotFoundError:
                # 逐级建目录
                parts = rel.split("/")
                cur = "/tmp/webdist"
                for p in parts[:-1]:
                    cur = cur + "/" + p
                    try:
                        sftp.mkdir(cur)
                    except IOError:
                        pass
                sftp.put(local_path, remote_path)
            count += 1
    sftp.close()
    print(f"上传完成: {count} 个文件")
    show("安装 web 文件", *run(ssh, "cp -r /tmp/webdist/* /root/seeinp/web/ && rm -rf /tmp/webdist && ls /root/seeinp/web/ && ls /root/seeinp/web/assets/ | head -5", sudo_pwd=PS_PWD))

    show("检查旧本地账号文件", *run(ssh, "ls -la /root/seeinp/data/ 2>/dev/null; cat /root/seeinp/data/local-user.json 2>/dev/null | head -3 || echo '(无 local-user.json)'", sudo_pwd=PS_PWD))

    show("启动 seeinps", *run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & sleep 5; ps aux | grep -v grep | grep seeinps", sudo_pwd=PS_PWD))
    show("端口监听", *run(ssh, "ss -tlnp | grep ':65443'", sudo_pwd=PS_PWD))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("日志尾部", *run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log", sudo_pwd=PS_PWD))
finally:
    ssh.close()

print("\n========== 4. 回到 seeinpm 服务器: 验证注册与代理 ==========")
time.sleep(3)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("在线客户端", *run(ssh, "tail -n 15 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()

print("\n部署完成")
