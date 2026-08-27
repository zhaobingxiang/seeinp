# -*- coding: utf-8 -*-
"""部署 0827-v3: 运维代理(ops_http)功能上线 + 端口池 UNIQUE 修复。
同步升级 seeinpm 和 seeinps（流头协议硬性约束：双端同步）。
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
    print((out or err).strip()[:1200])


print("========== 1. seeinpm 服务器: 停止 + 换二进制 + 重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("停止 seeinpm", *run(ssh, "pkill -f './seeinpm'; sleep 1; ps aux | grep -v grep | grep seeinpm || echo '已停止'"))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PM_BIN, "/tmp/seeinpm.new")
    sftp.close()
    show("替换二进制", *run(ssh, "sh -c 'mv /tmp/seeinpm.new /root/seeinp/seeinpm && chmod +x /root/seeinp/seeinpm && md5sum /root/seeinp/seeinpm'"))
    show("启动 seeinpm", *run(ssh, "sh -c 'cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 < /dev/null &'; sleep 3; ps aux | grep -v grep | grep seeinpm"))
    show("健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
finally:
    ssh.close()

print("\n========== 2. seeinps 服务器: 停止 + 换二进制 + 换前端 + 重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止 seeinps", *run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep -v grep | grep seeinps || echo '已停止'", sudo_pwd=PS_PWD))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    print("二进制上传完成")
    run(ssh, "rm -rf /tmp/webdist && mkdir -p /tmp/webdist/assets", sudo_pwd=PS_PWD)
    count = 0
    for root, _dirs, files in os.walk(LOCAL_PS_WEB):
        for fn in files:
            local_path = os.path.join(root, fn)
            rel = os.path.relpath(local_path, LOCAL_PS_WEB).replace("\\", "/")
            sftp.put(local_path, f"/tmp/webdist/{rel}")
            count += 1
    sftp.close()
    print(f"前端上传完成: {count} 个文件")
    show("替换二进制", *run(ssh, "sh -c 'mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps'", sudo_pwd=PS_PWD))
    show("安装 web 前端", *run(ssh, "sh -c 'rm -rf /root/seeinp/web && cp -r /tmp/webdist /root/seeinp/web && rm -rf /tmp/webdist && ls /root/seeinp/web/'", sudo_pwd=PS_PWD))
    show("数据库结构(旧库自动补列)", *run(ssh, "ls -la /root/seeinp/data/ 2>/dev/null", sudo_pwd=PS_PWD))
    show("启动 seeinps", *run(ssh, "sh -c 'cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null &'; sleep 5; ps aux | grep -v grep | grep seeinps", sudo_pwd=PS_PWD))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("日志尾部(应有 Registered OK + 代理恢复)", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinps.log", sudo_pwd=PS_PWD))
finally:
    ssh.close()

print("\n========== 3. seeinpm 侧验证注册与代理恢复 ==========")
time.sleep(2)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("seeinpm 日志尾部", *run(ssh, "tail -n 15 /root/seeinp/logs/seeinpm.log"))
    show("公网端口监听", *run(ssh, "ss -tlnp | grep seeinpm"))
finally:
    ssh.close()

print("\n部署完成")
