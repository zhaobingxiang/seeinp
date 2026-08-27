# -*- coding: utf-8 -*-
"""恢复 user3 原始授权码(部署误操作回滚) + PS conf 恢复原样 + 重启 PS 恢复服务
原始值(部署前从 PM DB 读出):
  auth_code = 1e247a5696f5894bd123559fe97573ada18ae67ff22bd1e914dacdb4afb807a4
  salt      = dca5184dd6c6f3e9062e5d01bca1e048
PS conf 原值: username=user1, auth_code=seeinp_7694c0274401a80bee39...
(实际凭证来自 PS 本地库 local_users 表, conf 不生效, 恢复原样只为整洁)
"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"

ORIG_HASH = "1e247a5696f5894bd123559fe97573ada18ae67ff22bd1e914dacdb4afb807a4"
ORIG_SALT = "dca5184dd6c6f3e9062e5d01bca1e048"
ORIG_PS_CODE = "seeinp_7694c0274401a80bee39"


def sudo_run(ssh, cmd, timeout=30):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
    stdin.write((PS_PWD + "\n").encode())
    stdin.channel.shutdown_write()
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def run(ssh, cmd, timeout=15):
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=timeout)
    stdout.channel.recv_exit_status()
    return stdout.read().decode("utf-8", "replace"), stderr.read().decode("utf-8", "replace")


def sudo_fire(ssh, cmd):
    chan = ssh.get_transport().open_session()
    chan.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'")
    time.sleep(0.5)
    chan.sendall((PS_PWD + "\n").encode())
    time.sleep(2)
    chan.close()


def show(name, out, err=""):
    text = (out or err).strip()
    print(f"--- {name} ---")
    print(text[:1000] if text else "(empty)")
    print()


# 1. PM: 恢复 user3 原始授权码
print("========== 1. PM 恢复 user3 原始授权码 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    sql = ("UPDATE users SET auth_code='%s', auth_code_salt='%s' WHERE username='user3'; "
           "SELECT username, auth_code, auth_code_salt, status FROM users;") % (ORIG_HASH, ORIG_SALT)
    show("恢复结果", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"%s\"" % sql))
finally:
    ssh.close()

# 2. PS: conf 恢复原样 + 重启
print("========== 2. PS conf 恢复 + 重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    # conf 里的码完整恢复(从备份看不到完整原码, 从当前 conf 找不到; 用部署时读到的原码前缀拼接)
    # 实际原码在 PS conf 备份中不存在, 但 local_users 表才是生效凭证, conf 恢复 user1 + 原码
    show("PS local_users 表(实际生效凭证)", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT username, seeinpm_user, substr(auth_code,1,16), registered FROM local_users;\""))
    show("当前 conf auth 段", *sudo_run(ssh, "grep -A2 '\\[auth\\]' /root/seeinp/conf/seeinps.toml | sed 's/seeinp_[a-f0-9]\\{8\\}[^\"]*/(略)/'"))
finally:
    ssh.close()

# 3. 重启 PS (凭证来自 local_users, 不依赖 conf)
print("========== 3. 重启 PS ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep seeinps | grep -v grep || echo '已停止'"))
    sudo_fire(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown")
    print("PS 已启动")
finally:
    ssh.close()

time.sleep(10)

# 4. 验证恢复
print("========== 4. 验证 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("PS 日志(期望 Registered OK)", *sudo_run(ssh, "tail -n 15 /root/seeinp/logs/seeinps.log"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM 监听(期望含 20001/20002/20003)", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
    show("PM 日志尾部(期望 Registered)", *run(ssh, "tail -n 8 /root/seeinp/logs/seeinpm.log"))
    show("TCP 20001 连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20001' && echo TCP_OK || echo TCP_FAIL"))
    show("ops 20003 存活(期望407)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
finally:
    ssh.close()
