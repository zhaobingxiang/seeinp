# -*- coding: utf-8 -*-
"""全面检查: seeinps/seeinpm 状态 + test 运维代理配置 + 内网目标可达性"""
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
    print((out or err).strip()[:2000])


print("========== seeinps (内网服务器) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("代理列表", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, proxy_username, public_port, acl, local_addr, local_port FROM local_proxies;'"))
    show("PS直连内网目标192.168.0.70", *run(ssh, "curl -s -m 8 -o /dev/null -w '%{http_code}' http://192.168.0.70/ ; echo ''"))
    show("seeinps日志尾部", *sudo_run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

print("\n========== seeinpm (公网服务器) ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("进程", *run(ssh, "ps aux | grep seeinpm | grep -v grep || echo '未运行!'"))
    show("端口监听", *run(ssh, "ss -tlnp | grep -E 'seeinpm'"))
    show("分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, proxy_type, status FROM port_allocations ORDER BY port;'"))
    show("日志尾部", *run(ssh, "tail -n 20 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
