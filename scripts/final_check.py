# -*- coding: utf-8 -*-
"""最终状态确认: 双端健康/注册/端口/代理数据"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


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
    print(f"--- {name} ---")
    print((out or err).strip()[:1200])
    print()


print("========== seeinps ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("代理数据(端口已同步)", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, proxy_username, public_port FROM local_proxies;'"))
finally:
    ssh.close()

print("========== seeinpm ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
    show("监听端口", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
    show("分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, proxy_type, status FROM port_allocations ORDER BY port;'"))
    show("用户在线状态", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT username, CASE WHEN online_session IS NULL THEN \"offline\" ELSE \"online\" END FROM users;'"))
    show("复测代理(期望200)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}' http://192.168.0.70/"))
finally:
    ssh.close()
