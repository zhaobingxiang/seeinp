# -*- coding: utf-8 -*-
"""针对性排查: TLS 访问 + 两台服务器日志 + seeinpm 服务器上的 seeinps 进程。"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def run(ssh, cmd, sudo_pwd=None, timeout=15):
    if sudo_pwd:
        stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
        stdin.write((sudo_pwd + "\n").encode())
        stdin.channel.shutdown_write()
    else:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:2000])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("TLS方式访问 /health", *run(ssh, "curl -sk -m 5 https://127.0.0.1:999/health; echo"))
    show("TLS方式访问首页状态码", *run(ssh, "curl -sk -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' https://127.0.0.1:999/; echo"))
    show("seeinpm 配置文件", *run(ssh, "cat /root/seeinp/conf/seeinpm.toml"))
    show("seeinpm 日志尾部", *run(ssh, "tail -n 40 /root/seeinp/logs/seeinpm.log"))
    show("logs目录清单", *run(ssh, "ls -la /root/seeinp/logs/"))
    show("本机seeinps进程详情", *run(ssh, "ps aux | grep -v grep | grep seeinps; ss -tnp | grep -E ':999\\s' | head -10"))
    show("本机seeinps日志尾部", *run(ssh, "tail -n 40 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

print("\n" + "#" * 60 + "\n# seeinps 内网服务器\n" + "#" * 60)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("logs目录清单", *run(ssh, "ls -la /root/seeinp/logs/", sudo_pwd=PS_PWD))
    show("全部日志尾部", *run(ssh, "find /root/seeinp/logs -type f -name '*.log' -exec tail -n 30 {} +", sudo_pwd=PS_PWD))
    show("seeinpc进程详情", *run(ssh, "ps aux | grep -v grep | grep seeinpc; ls -la /root/seeinp/ 2>/dev/null; which seeinpc 2>/dev/null", sudo_pwd=PS_PWD))
    show("本机IP", *run(ssh, "ip addr | grep 'inet ' | grep -v 127.0.0.1", sudo_pwd=PS_PWD))
finally:
    ssh.close()
