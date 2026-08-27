# -*- coding: utf-8 -*-
"""上传 web-pm/dist 到 seeinpm 服务器并验证。"""
import os

import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
LOCAL_DIST = r"d:\ide\seeinp\web-pm\dist"
REMOTE_DIR = "/root/seeinp/web-pm/dist"


def run(ssh, cmd, timeout=20):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:800])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("清理旧 dist", *run(ssh, f"rm -rf {REMOTE_DIR} && mkdir -p {REMOTE_DIR}/assets"))
    sftp = ssh.open_sftp()
    count = 0
    for root, _dirs, files in os.walk(LOCAL_DIST):
        for fn in files:
            local_path = os.path.join(root, fn)
            rel = os.path.relpath(local_path, LOCAL_DIST).replace("\\", "/")
            remote_path = f"{REMOTE_DIR}/{rel}"
            sftp.put(local_path, remote_path)
            count += 1
    sftp.close()
    print(f"上传完成: {count} 个文件")
    show("服务器文件清单", *run(ssh, f"ls -la {REMOTE_DIR}/ && ls {REMOTE_DIR}/assets/"))
    show("首页响应", *run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' http://127.0.0.1:9998/"))
    show("首页引用的JS", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/ | grep -oE 'assets/[^\"]+\\.js'"))
    show("Dashboard chunk 中文验证", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/assets/Dashboard-C-jkChXr.js | grep -o '仪表盘' | head -1; echo '---'; curl -s -m 5 http://127.0.0.1:9998/assets/Users-Jpe_YZO2.js | grep -o '用户创建成功' | head -1"))
finally:
    ssh.close()
