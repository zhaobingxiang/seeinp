# -*- coding: utf-8 -*-
"""核实 seeinpm B端 API(9998) 与前端静态服务。"""
import paramiko

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect("170.106.109.105", port=22, username="root", password="Zbx.9705", timeout=10)
try:
    for name, cmd in [
        ("seeinpm 全部监听端口", "ss -tlnp | grep seeinpm"),
        ("9998 /health", "curl -s -m 5 http://127.0.0.1:9998/health; echo"),
        ("9998 首页", "curl -s -m 5 -o /dev/null -w '%{http_code} (%{size_download}B)' http://127.0.0.1:9998/; echo"),
        ("9998 首页内容(前200字)", "curl -s -m 5 http://127.0.0.1:9998/ | head -c 200; echo"),
        ("9998 静态资源", "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:9998/assets/ -o /dev/null; ls /root/seeinp/web-pm/dist/assets/ 2>/dev/null | head -5"),
        ("9998 外网可达性(从公网口)", "curl -s -m 5 http://170.106.109.105:9998/health; echo"),
        ("db中的用户", "command -v sqlite3 >/dev/null && sqlite3 /root/seeinp/data/seeinpm.db 'select username,status from users;' 2>&1 || echo '<< 无 sqlite3 >>'"),
        ("seeinpm 完整日志(前60行)", "head -n 60 /root/seeinp/logs/seeinpm.log"),
    ]:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=15)
        stdout.channel.recv_exit_status()
        out = (stdout.read() + stderr.read()).decode("utf-8", "replace")
        print(f"\n--- {name} ---")
        print(out.strip()[:1800])
finally:
    ssh.close()
