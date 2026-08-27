# -*- coding: utf-8 -*-
"""修复 v3 部署: sudo 包裹完整命令链，补 chmod + web 安装 + 启动。"""
import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"


def sudo_run(ssh, cmd, timeout=30):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c '" + cmd + "'", timeout=timeout)
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
    print((out or err).strip()[:1200])


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("当前状态", *run(ssh, "ls -la /root/seeinp/ 2>&1 | head -5; echo ---; ls /root/seeinp/web 2>&1 | head -3"))
    show("修复二进制权限", *sudo_run(ssh, "chmod +x /root/seeinp/seeinps && md5sum /root/seeinp/seeinps && ls -la /root/seeinp/seeinps"))
    show("重装 web(从 /tmp/webdist 残留)", *sudo_run(ssh, "ls /tmp/webdist 2>/dev/null && rm -rf /root/seeinp/web && cp -r /tmp/webdist /root/seeinp/web && ls /root/seeinp/web/ || echo 'webdist 已被清理'"))
    show("启动 seeinps", *sudo_run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & sleep 5; ps aux | grep -v grep | grep seeinps || echo '启动失败'"))
    show("B端健康检查", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health"))
    show("初始化状态", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
    show("日志尾部", *sudo_run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

print("\n========== seeinpm 侧验证重连 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("seeinpm 日志尾部", *run(ssh, "tail -n 8 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()
