# -*- coding: utf-8 -*-
"""从 PS 服务器(不同网络路径)走公网IP绕一圈测运维代理 — 全链路验证"""
import paramiko

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect('see.timemsee.cn', port=27141, username='hik', password='Zbx.9705', timeout=10)
cmds = [
    ('①PS服务器经公网IP代理访问192.168.0.70(全链路)', "curl -s -m 20 -x http://170.106.109.105:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code} time=%{time_total}s\\n' http://192.168.0.70/"),
    ('②PS服务器出口IP', 'curl -s -m 8 http://ifconfig.me 2>/dev/null || echo skip'),
]
try:
    for name, cmd in cmds:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=30)
        stdout.channel.recv_exit_status()
        out = stdout.read().decode('utf-8', 'replace').strip()
        err = stderr.read().decode('utf-8', 'replace').strip()
        print('---', name, '---')
        print((out or err)[:300])
        print()
finally:
    ssh.close()
