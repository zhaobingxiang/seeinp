# -*- coding: utf-8 -*-
"""服务器端运维代理完整链路验证: 插入测试代理 -> 重启 -> 407/认证/ACL/转发测试。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
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


print("========== 1. 插入测试代理 e2e-check ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    sql = ("INSERT INTO local_proxies (proxy_id, type, local_addr, local_port, public_port, proxy_username, proxy_password, acl, status, created_at, updated_at) "
           "VALUES ('e2e-check', 'ops_http', '', 0, 0, 'e2euser', '" + BCRYPT + "', '[\"127.0.0.0/8\",\"10.0.0.0/8\"]', 1, strftime('%s','now'), strftime('%s','now'));")
    show("删除旧测试记录", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"DELETE FROM local_proxies WHERE proxy_id='e2e-check';\""))
    show("插入测试代理", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"" + sql.replace('"', '\\"') + "\""))
    show("确认插入", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, type, proxy_username, acl FROM local_proxies;\""))
    show("重启 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown; echo relaunched"))
finally:
    ssh.close()

time.sleep(6)

print("\n========== 2. 验证代理恢复与端口分配 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("日志(期望 e2e-check 分配端口)", *sudo_run(ssh, "tail -n 8 /root/seeinp/logs/seeinps.log"))
    show("数据库端口", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, public_port FROM local_proxies WHERE proxy_id='e2e-check';\""))
finally:
    ssh.close()

print("\n========== 3. seeinpm 侧确认监听 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("端口监听", *run(ssh, "ss -tlnp | grep seeinpm"))
finally:
    ssh.close()

print("\n========== 4. 完整链路测试(从内网服务器发起) ==========")
PORT = None
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, _ = sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT public_port FROM local_proxies WHERE proxy_id='e2e-check';\"")
    PORT = out.strip()
    print(f"e2e-check 端口: {PORT}")
    if PORT:
        px = f"http://170.106.109.105:{PORT}"
        show("4.1 无认证(期望407)", *run(ssh, f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' -x {px} http://127.0.0.1:65443/health"))
        show("4.2 认证+ACL允许(期望200+health JSON)", *run(ssh, f"curl -s -m 8 -x http://e2euser:E2eTest123!@170.106.109.105:{PORT} http://127.0.0.1:65443/health"))
        show("4.3 认证+ACL拒绝(期望403)", *run(ssh, f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' -x http://e2euser:E2eTest123!@170.106.109.105:{PORT} http://www.baidu.com/"))
        show("4.4 错误密码(期望407)", *run(ssh, f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' -x http://e2euser:WrongPass1!@170.106.109.105:{PORT} http://127.0.0.1:65443/health"))
        show("4.5 seeinps 访问日志", *sudo_run(ssh, "grep OPS /root/seeinp/logs/seeinps.log | tail -n 6"))
finally:
    ssh.close()
