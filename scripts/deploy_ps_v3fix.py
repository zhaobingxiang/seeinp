# -*- coding: utf-8 -*-
"""部署 seeinps v3 + 更新用户ACL + 插入验证代理。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
LOCAL_PS_BIN = r"d:\ide\seeinp\seeinps-linux-amd64"
BCRYPT = "$2a$10$D62UvqZB5wzXoBJCByyJlug0ajBehKdO4rLgVQA2NF1StOBgg23fa"


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
    print((out or err).strip()[:1500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; echo stopped"))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PS_BIN, "/tmp/seeinps.new")
    sftp.close()
    print("二进制上传完成")
    show("替换二进制", *sudo_run(ssh, "mv /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps"))
    # 用户 http 代理 ACL 增加 192.168.0.0/16 (用户要测 192.168.0.70)
    show("更新用户ACL", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"UPDATE local_proxies SET acl='[\\\"10.0.0.0/8\\\",\\\"172.16.0.0/12\\\",\\\"192.168.0.0/16\\\"]', local_addr='', local_port=0 WHERE proxy_id='http';\" && sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, acl, local_addr, local_port FROM local_proxies WHERE proxy_id=\\\"http\\\";'"))
    # 插入验证代理(不动用户密码)
    show("插入验证代理", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"DELETE FROM local_proxies WHERE proxy_id='e2e-verify'; INSERT INTO local_proxies (proxy_id, type, local_addr, local_port, public_port, proxy_username, proxy_password, acl, status, created_at, updated_at) VALUES ('e2e-verify', 'ops_http', '', 0, 0, 'e2euser', '" + BCRYPT + "', '[\\\"192.168.0.0/16\\\"]', 1, strftime('%s','now'), strftime('%s','now'));\" && echo inserted"))
    show("确认数据", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, proxy_username, public_port FROM local_proxies;'"))
finally:
    ssh.close()

# 启动(独立连接)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("启动 seeinps", *run(ssh, "sudo -S sh -c 'cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown; echo launched'"))
finally:
    ssh.close()

time.sleep(7)

print("\n========== 验证端口恢复(核心: 历史端口复用) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("启动日志(期望 proxy2->20001 proxy3->20002 http->20003)", *sudo_run(ssh, "grep -E 'ALLOC|POOL|Registered' /root/seeinp/logs/seeinps.log | tail -n 10"))
finally:
    ssh.close()

print("\n========== seeinpm 侧端口监听 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
del ssh
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect("170.106.109.105", port=22, username="root", password="Zbx.9705", timeout=10)
try:
    show("端口监听(期望20001-20004)", *run(ssh, "ss -tlnp | grep seeinpm"))
    show("分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, status FROM port_allocations ORDER BY port;'"))
    show("日志尾部", *run(ssh, "tail -n 10 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
