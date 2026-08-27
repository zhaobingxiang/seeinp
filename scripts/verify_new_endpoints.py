# -*- coding: utf-8 -*-
"""验证新 API 端点已部署(未登录探测: 新版返回401, 旧版404) + 版本信息"""
import paramiko

PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def sudo_run(ssh, cmd, timeout=25):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    # 新端点探测: 期望 401(路由存在但未登录); 旧版是 404
    for path in ["/api/v1/users/user3/disable", "/api/v1/users/user3/enable", "/api/v1/users/user3/reset-code"]:
        out, _ = run(ssh, "curl -s -o /dev/null -w '%%{http_code}' -X POST http://127.0.0.1:9998%s" % path)
        print("POST %-40s -> %s (401=新版已部署)" % (path, out.strip()))
    out, _ = run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health")
    print("\nhealth:", out.strip())
    # 前端新页面探测: Users 组件 js 已包含 disable/enable
    out, _ = run(ssh, "grep -l 'reset-code' /root/seeinp/web-pm/dist/assets/*.js | head -n 2")
    print("前端包含 reset-code 的文件:", out.strip() or "(未找到!)")
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    out, _ = sudo_run(ssh, "md5sum /root/seeinp/seeinps /root/seeinp/seeinps.bak.0801 | awk '{print $1}'")
    lines = out.strip().split("\n")
    same = "相同(未更新!)" if len(lines) == 2 and lines[0] == lines[1] else "不同(已更新)"
    print("\nPS 新旧二进制 md5:", same)
finally:
    ssh.close()
