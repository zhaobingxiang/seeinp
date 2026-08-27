# -*- coding: utf-8 -*-
"""部署 v3 修复: seeinpm(端口稳定) + 数据库端口恢复。"""
import time

import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
LOCAL_PM_BIN = r"d:\ide\seeinp\seeinpm-linux-amd64"


def run(ssh, cmd, timeout=20):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("当前分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, status FROM port_allocations ORDER BY port;'"))
    show("停止 seeinpm", *run(ssh, "pkill -f './seeinpm'; sleep 1; echo done"))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PM_BIN, "/tmp/seeinpm.new")
    sftp.close()
    show("替换二进制", *run(ssh, "mv /tmp/seeinpm.new /root/seeinp/seeinpm && chmod +x /root/seeinp/seeinpm && md5sum /root/seeinp/seeinpm"))
    # 恢复用户原端口: http->20003, proxy2->20001, proxy3->20002 (status=0 模拟释放, 验证历史端口复用)
    show("修正端口记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"UPDATE port_allocations SET port=20003, status=0, released_at=strftime('%s','now') WHERE proxy_id='http'; UPDATE port_allocations SET port=20001, status=0, released_at=strftime('%s','now') WHERE proxy_id='proxy2'; UPDATE port_allocations SET port=20002, status=0, released_at=strftime('%s','now') WHERE proxy_id='proxy3';\" && sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, status FROM port_allocations ORDER BY port;'"))
finally:
    ssh.close()

# 启动(独立连接执行, 避免通道挂起)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("启动 seeinpm", *run(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 < /dev/null & disown; echo launched"))
finally:
    ssh.close()

time.sleep(4)

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinpm | grep -v grep || echo '未运行!'"))
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '无响应'"))
finally:
    ssh.close()
print("\nseeinpm v3 部署完成")
