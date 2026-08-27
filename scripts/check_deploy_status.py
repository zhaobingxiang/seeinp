# -*- coding: utf-8 -*-
"""校验测试服务器上 seeinpm / seeinps 的部署与运行状态。"""
import hashlib
import sys

import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"

MD5_LOCAL_PM = hashlib.md5(open(r"d:\ide\seeinp\seeinpm-linux-amd64", "rb").read()).hexdigest()
MD5_LOCAL_PS = hashlib.md5(open(r"d:\ide\seeinp\seeinps-linux-amd64", "rb").read()).hexdigest()


def run(ssh, cmd, sudo_pwd=None, timeout=15):
    if sudo_pwd:
        stdin, stdout, stderr = ssh.exec_command("sudo -S " + cmd, timeout=timeout)
        stdin.write((sudo_pwd + "\n").encode())
        stdin.channel.shutdown_write()
    else:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def section(title):
    print("\n" + "=" * 60)
    print(title)
    print("=" * 60)


def check_pm():
    section("seeinpm 服务器 170.106.109.105")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        checks = [
            ("进程状态", "ps aux | grep -v grep | grep seeinpm || echo '<< seeinpm 未运行 >>'"),
            ("systemd 服务", "systemctl is-active seeinpm 2>&1; systemctl list-units --type=service | grep -i seein || echo '<< 无 systemd 服务 >>'"),
            ("监听端口(999/654)", "ss -tlnp | grep -E ':(999|65443)\\b' || echo '<< 999 未监听 >>'"),
            ("已分配端口监听数(20000-30000)", "ss -tlnp | awk '{print $4}' | grep -oE '[0-9]+$' | awk '$1>=20000 && $1<=30000' | wc -l"),
            ("本机健康检查", "curl -s -m 5 http://127.0.0.1:999/health || echo '<< /health 无响应 >>'"),
            ("本机前端首页", "curl -s -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' http://127.0.0.1:999/ || echo '<< / 无响应 >>'"),
            ("部署目录", "ls -la /root/seeinp/ 2>&1 | head -20"),
            ("二进制信息(md5)", "md5sum /root/seeinp/seeinpm 2>/dev/null || find / -name seeinpm -type f 2>/dev/null | head -3"),
            ("前端dist", "ls /root/seeinp/web-pm/dist/ 2>&1 | head -10; ls /root/seeinp/web/ 2>&1 | head -5"),
            ("数据库", "ls -la /root/seeinp/data/ 2>&1"),
            ("日志尾部(30行)", "tail -30 /root/seeinp/logs/*.log 2>&1 | tail -40"),
            ("系统时间/负载", "date; uptime"),
        ]
        for name, cmd in checks:
            out, err = run(ssh, cmd)
            print(f"\n--- {name} ---")
            print((out or err).strip()[:1500])
        print(f"\n--- 版本比对 ---")
        print(f"本地 seeinpm-linux-amd64 md5: {MD5_LOCAL_PM}")
    finally:
        ssh.close()


def check_ps():
    section("seeinps 服务器 see.timemsee.cn:27141 (内网 192.168.0.10)")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        checks = [
            ("进程状态", "ps aux | grep -v grep | grep seeinps || echo '<< seeinps 未运行 >>'"),
            ("监听端口(65443)", "ss -tlnp | grep ':65443' || echo '<< 65443 未监听 >>'"),
            ("到 seeinpm 的控制连接", "ss -tnp | grep ':999 ' || echo '<< 无到 170.106.109.105:999 的连接 >>'"),
            ("本机健康检查", "curl -s -m 5 http://127.0.0.1:65443/health || echo '<< /health 无响应 >>'"),
            ("本机API状态", "curl -s -m 5 http://127.0.0.1:65443/api/v1/status || echo '<< /api/v1/status 无响应 >>'"),
            ("部署目录", "ls -la /root/seeinp/ 2>&1 | head -20"),
            ("二进制md5", "md5sum /root/seeinp/seeinps 2>/dev/null"),
            ("配置文件", "cat /root/seeinp/conf/seeinps.toml 2>&1"),
            ("日志尾部(40行)", "tail -40 /root/seeinp/logs/*.log 2>&1 | tail -50"),
            ("系统时间", "date"),
        ]
        for name, cmd in checks:
            out, err = run(ssh, cmd, sudo_pwd=PS_PWD)
            print(f"\n--- {name} ---")
            print((out or err).strip()[:1500])
        print(f"\n--- 版本比对 ---")
        print(f"本地 seeinps-linux-amd64 md5: {MD5_LOCAL_PS}")
    finally:
        ssh.close()


if __name__ == "__main__":
    try:
        check_pm()
    except Exception as e:
        print(f"\n!! seeinpm 检查失败: {e}")
    try:
        check_ps()
    except Exception as e:
        print(f"\n!! seeinps 检查失败: {e}")
