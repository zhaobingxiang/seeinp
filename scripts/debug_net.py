# -*- coding: utf-8 -*-
"""深挖: 进程状态 + 重启日志 + 服务器网络环境。"""
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
    print((out or err).strip()[:2500])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("进程(单独执行)", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '进程不存在!'"))
    show("B端健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo 'B端无响应'"))
    show("日志开头(重启启动段)", *sudo_run(ssh, "grep -n 'Listening on' /root/seeinp/logs/seeinps.log | tail -3"))
    show("重启后非OPS日志", *sudo_run(ssh, "grep -v 'OPS' /root/seeinp/logs/seeinps.log | tail -n 20"))
    print("\n========== 服务器网络环境 ==========")
    show("网卡(ifconfig)", *run(ssh, "ifconfig 2>/dev/null | grep -E 'inet |UP' || cat /proc/net/fib_trie 2>/dev/null | grep -A1 'Local:' | head -20"))
    show("路由表", *run(ssh, "cat /proc/net/route 2>/dev/null | head -15 || route -n 2>/dev/null"))
    show("测试 ping 网关段", *run(ssh, "timeout 3 ping -c 2 10.0.52.61 2>&1 | tail -2 || echo 'ping失败'"))
    show("测试 curl 127.0.0.1:65443(本机B端)", *run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:65443/health"))
finally:
    ssh.close()
