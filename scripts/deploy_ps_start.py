# -*- coding: utf-8 -*-
"""补: root 权限 chmod + 启动 seeinps + 验证。"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def run(ssh, cmd, timeout=30):
    stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1200])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("赋执行权限+校验", *run(ssh, "chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps && ls -la /root/seeinp/seeinps"))

    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c 'cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null &'", timeout=10)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    time.sleep(5)

    show("进程状态", *run(ssh, "ps aux | grep -v grep | grep seeinps || echo '<< 未运行 >>'"))
    show("端口监听", *run(ssh, "ss -tlnp | grep ':65443' || echo '<< 65443 未监听 >>'"))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '<< 无响应 >>'"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status || echo '<< 无响应 >>'"))
    show("连接状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/status || echo '<< 无响应 >>'"))
    show("日志尾部", *run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()
