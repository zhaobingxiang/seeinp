# -*- coding: utf-8 -*-
"""sudo 验证服务器 Proxies.js 内容 + 检查访问入口。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=20):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + repr(cmd), timeout=timeout)
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
    show("Proxies.js 运维关键词计数(修正版)", *sudo_run(ssh, "grep -c 'proxyUsername' /root/seeinp/web/assets/Proxies-BQm9UB73.js; grep -o 'ops_http' /root/seeinp/web/assets/Proxies-BQm9UB73.js | head -2; grep -o 'opsId' /root/seeinp/web/assets/Proxies-BQm9UB73.js | head -2"))
    show("md5 对比", *sudo_run(ssh, "md5sum /root/seeinp/web/assets/Proxies-BQm9UB73.js /root/seeinp/web/index.html /root/seeinp/web/assets/index-Bacf4wvv.js"))
    show("B端监听地址", *sudo_run(ssh, "ss -tlnp | grep 65443"))
    show("B端可从外部访问?(绑定测试)", *run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code}' http://$(hostname -I | awk '{print $1}'):65443/ || echo fail"))
finally:
    ssh.close()
