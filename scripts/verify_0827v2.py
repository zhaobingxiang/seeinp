# -*- coding: utf-8 -*-
"""最终验证: pm 注册记录 + ps web 页面 + 前端资源。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def run(ssh, cmd, sudo_pwd=None, timeout=20):
    if sudo_pwd:
        stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
        stdin.write((sudo_pwd + "\n").encode())
        stdin.channel.shutdown_write()
    else:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def show(name, out, err=""):
    print(f"--- {name} ---")
    print((out or err).strip()[:1200])


print("========== seeinpm 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("pm 日志(user3 注册记录)", *run(ssh, "tail -n 12 /root/seeinp/logs/seeinpm.log"))
    show("端口池状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
finally:
    ssh.close()

print("\n========== seeinps 侧 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("web 首页", *run(ssh, "curl -s -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' http://127.0.0.1:65443/"))
    show("首页 JS 引用", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/ | grep -oE 'assets/[^\"]+\\.(js|css)' | head -4"))
    show("Proxies 页面中文验证", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/assets/Proxies-*.js | grep -o '转发端口' | head -1"))
    show("代理接口鉴权(期望401)", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/proxies"))
finally:
    ssh.close()
