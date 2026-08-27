# -*- coding: utf-8 -*-
"""修复: ACL 更新(单引号SQL) + 正确启动 seeinps + 验证。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=25):
    stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
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


def shq(sql):
    return "'" + sql.replace("'", "'\"'\"'") + "'"


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    sql_acl = "UPDATE local_proxies SET acl=" + shq('["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"]') + ", local_addr='', local_port=0 WHERE proxy_id='http';"
    show("更新用户ACL", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db " + shq(sql_acl)))
    show("确认ACL", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db " + shq("SELECT proxy_id, acl FROM local_proxies WHERE proxy_id='http';")))
    show("确认全部代理", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db " + shq("SELECT proxy_id, type, proxy_username, public_port FROM local_proxies;")))
    show("启动 seeinps", *sudo_run(ssh, "sh -c 'cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown'; echo launched"))
finally:
    ssh.close()

time.sleep(7)

print("\n========== 验证 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("启动日志", *sudo_run(ssh, "grep -E 'ALLOC|POOL|Registered|Connecting|Handshake' /root/seeinp/logs/seeinps.log | tail -n 10"))
finally:
    ssh.close()

time.sleep(2)
print("\n========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("端口监听", *run(ssh, "ss -tlnp | grep seeinpm"))
    show("分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, status FROM port_allocations ORDER BY port;'"))
    show("日志尾部", *run(ssh, "tail -n 8 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
