# -*- coding: utf-8 -*-
"""部署前检查: PM/PS 当前版本、进程、用户列表、代理状态"""
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
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1500])


def pm():
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        show("PM进程", *run(ssh, "ps aux | grep seeinpm | grep -v grep || echo '未运行'"))
        show("PM监听端口", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
        show("PM用户表", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT id, username, status, substr(coalesce(online_session,\"\"),1,12) FROM users;'"))
        show("PM端口分配", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, user_id, proxy_id, proxy_type, status FROM port_allocations ORDER BY port;'"))
        show("web-pm/dist 时间", *run(ssh, "ls -la /root/seeinp/web-pm/dist/ 2>/dev/null | head -n 5 || echo '无dist'"))
    finally:
        ssh.close()


def ps():
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        show("PS进程", *sudo_run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行'"))
        show("PS配置的用户", *sudo_run(ssh, "grep -A2 '\\[auth\\]' /root/seeinp/conf/seeinps.toml | grep -v auth_code"))
        show("PS日志尾部", *sudo_run(ssh, "tail -n 5 /root/seeinp/logs/seeinps.log"))
        show("PS本地代理", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, public_port FROM local_proxies;'"))
    finally:
        ssh.close()


if __name__ == "__main__":
    pm()
    ps()
