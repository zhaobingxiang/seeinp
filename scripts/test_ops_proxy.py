# -*- coding: utf-8 -*-
"""运维HTTP代理端到端测试(公网服务器侧) - 使用正确的 -U 代理认证参数"""
import paramiko

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect('170.106.109.105', port=22, username='root', password='Zbx.9705', timeout=10)
tests = [
    ('①代理认证访问内网192.168.0.70(期望200)', "curl -s -m 15 -x http://127.0.0.1:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code} time=%{time_total}s\\n' http://192.168.0.70/"),
    ('②无认证(期望407)', "curl -s -m 15 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"),
    ('③错误密码(期望407)', "curl -s -m 15 -x http://127.0.0.1:20003 -U 'test:WrongPass123' -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"),
    ('④代理认证访问公网(期望403 ACL)', "curl -s -m 15 -x http://127.0.0.1:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://www.baidu.com/"),
    ('⑤经公网IP走代理(期望200)', "curl -s -m 15 -x http://170.106.109.105:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code} time=%{time_total}s\\n' http://192.168.0.70/"),
    ('⑥页面内容抽样', "curl -s -m 15 -x http://127.0.0.1:20003 -U 'test:Test@seeinp2026' http://192.168.0.70/ | head -c 300"),
    ('⑦CONNECT隧道测试 https 内网(若有https服务)', "curl -sk -m 15 -x http://127.0.0.1:20003 -U 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' https://192.168.0.70/"),
]
try:
    for name, cmd in tests:
        stdin, stdout, stderr = ssh.exec_command(cmd, timeout=25)
        stdout.channel.recv_exit_status()
        out = stdout.read().decode('utf-8', 'replace').strip()
        err = stderr.read().decode('utf-8', 'replace').strip()
        print('---', name, '---')
        print((out or err)[:500])
finally:
    ssh.close()
