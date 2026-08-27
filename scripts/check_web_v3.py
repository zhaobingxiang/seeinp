# -*- coding: utf-8 -*-
"""检查 PS 服务器 web 前端是否包含运维代理表单。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=20):
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


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("web 目录文件", *sudo_run(ssh, "ls -la /root/seeinp/web/ /root/seeinp/web/assets/"))
    show("首页内容", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/"))
    show("首页引用的JS", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/ | grep -oE 'assets/[^\"]+\\.(js|css)'"))
    show("Proxies chunk 是否含运维代理关键词", *run(ssh, "for f in /root/seeinp/web/assets/Proxies-*.js; do echo \"$f:\"; grep -c 'proxyUsername\\|ops_http\\|运维' \"$f\" 2>/dev/null || echo 0; done"))
    show("本地 dist 文件名对比", *run(ssh, "ls /root/seeinp/web/assets/ | grep -i proxies"))
finally:
    ssh.close()
