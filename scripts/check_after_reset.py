# -*- coding: utf-8 -*-
"""排查重置授权码后 seeinps 的实际状态: 进程/日志/web端口/本地库"""
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
    text = (out or err).strip()
    print(f"--- {name} ---")
    print(text[:1500] if text else "(empty)")
    print()


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("PS进程", *sudo_run(ssh, "ps aux | grep seeinps | grep -v grep || echo '进程已退出!'"))
    show("PS日志尾部(看终态日志)", *sudo_run(ssh, "tail -n 25 /root/seeinp/logs/seeinps.log"))
    show("PS本地web端口65443", *sudo_run(ssh, "ss -tlnp | grep 65443 || echo '未监听!'"))
    show("web本地可访问性", *sudo_run(ssh, "curl -s -m 5 -o /dev/null -w 'HTTP %{http_code}\\n' http://127.0.0.1:65443/ || echo '无响应'"))
    show("local_users状态", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT username, seeinpm_user, substr(auth_code,1,16), registered, must_change FROM local_users;\""))
    show("本地代理数据(应该还在)", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, type, public_port FROM local_proxies;\""))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM用户状态", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, status, substr(auth_code,1,16) FROM users;\""))
    show("PM端口分配", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, status FROM port_allocations ORDER BY port;'"))
    show("PM监听", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
finally:
    ssh.close()
