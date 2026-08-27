# -*- coding: utf-8 -*-
"""检查 seeinpm 当前状态(上一脚本重启步骤超时)。"""
import paramiko

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect("170.106.109.105", port=22, username="root", password="Zbx.9705", timeout=10)
try:
    for name, cmd in [
        ("进程", "ps aux | grep -v grep | grep -E 'seeinpm|seeinps' || echo '<< 无相关进程 >>'"),
        ("端口", "ss -tlnp | grep -E ':(999|9998)\\b' || echo '<< 999/9998 未监听 >>'"),
        ("日志尾部", "tail -n 15 /root/seeinp/logs/seeinpm.log 2>/dev/null || echo '<< 无日志 >>'"),
        ("健康检查", "curl -s -m 5 http://127.0.0.1:9998/health || echo '<< 无响应 >>'"),
        ("admin状态", "curl -s -m 5 -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:9998/api/v1/auth/init"),
    ]:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=15)
        stdout.channel.recv_exit_status()
        print(f"\n--- {name} ---")
        print((stdout.read() + stderr.read()).decode("utf-8", "replace").strip()[:1000])
finally:
    ssh.close()
