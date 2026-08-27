# -*- coding: utf-8 -*-
"""排查: 1) e2e-check 端口未分配原因 2) 内网目标设备连通性。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


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
    print((out or err).strip()[:2000])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("重启后完整日志(ALLOC记录)", *sudo_run(ssh, "grep -E 'ALLOC|Registered|Connecting|Handshake|failed' /root/seeinp/logs/seeinps.log | tail -n 20"))
    show("e2e-check 数据库状态", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, type, local_addr, local_port, public_port, status FROM local_proxies;\""))
    show("进程状态", *sudo_run(ssh, "ps aux | grep -v grep | grep seeinps"))
    print("\n========== 内网目标设备连通性(从 seeinps 服务器直接访问) ==========")
    for target in ["10.0.52.61:80", "10.1.127.188:1883", "172.16.10.100:80", "10.0.52.61:37527"]:
        show(f"直连 {target}", *run(ssh, f"timeout 5 bash -c 'echo > /dev/tcp/{target}' 2>&1 && echo 'TCP通' || echo 'TCP不通'"))
    show("本机网卡地址", *run(ssh, "ip -4 addr | grep inet | grep -v 127.0.0.1"))
finally:
    ssh.close()
