# -*- coding: utf-8 -*-
"""用户管理功能(v0.1.2)测试服务器部署:
阶段1: PS 上传新二进制 + conf 改为 user3/新码(不重启)
阶段2: PM 上传新二进制+web-pm/dist, sqlite 更新 user3 授权码, 重启 PM
阶段3: PS 重启(先用旧PM注册成功), 再无——顺序: PS先重启连旧PM, 紧接着PM重启
实际顺序: [1 PS上传+conf] → [2 PM上传+改库] → [3 PS重启→旧PM注册OK] → [4 PM重启] → [5 验证]
"""
import hashlib
import secrets
import sys
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"

PM_BIN = r"d:\ide\seeinp\dist\release\seeinpm_linux\seeinpm"
PS_BIN = r"d:\ide\seeinp\dist\release\seeinps_linux\seeinps"
WEB_PM_DIST = r"d:\ide\seeinp\web-pm\dist"


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


def fire(ssh, cmd):
    chan = ssh.get_transport().open_session()
    chan.exec_command(cmd)
    time.sleep(2)
    chan.close()


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
    print(text[:1200] if text else "(empty)")
    print()


def die(msg):
    print("[FATAL] " + msg)
    sys.exit(1)


def upload(sftp, local, remote):
    sftp.put(local, remote)


def upload_dir(sftp, local_dir, remote_dir):
    try:
        sftp.stat(remote_dir)
    except FileNotFoundError:
        sftp.mkdir(remote_dir)
    import os
    for name in os.listdir(local_dir):
        lp = os.path.join(local_dir, name)
        rp = remote_dir + "/" + name
        if os.path.isdir(lp):
            upload_dir(sftp, lp, rp)
        else:
            sftp.put(lp, rp)


def main():
    # 生成 user3 新授权码
    code = "seeinp_" + secrets.token_hex(32)
    salt = secrets.token_hex(16)
    code_hash = hashlib.sha256((salt + code).encode()).hexdigest()
    with open(r"d:\ide\seeinp\dist\release\user3_new_authcode.txt", "w") as f:
        f.write("username=user3\nauth_code=%s\n" % code)
    print("[*] user3 新授权码已生成并保存到 dist/release/user3_new_authcode.txt\n")

    # ===== 阶段1: PS 上传二进制 + 改 conf (不重启) =====
    print("========== 阶段1: PS 上传二进制 + 修改 conf ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        sftp = ssh.open_sftp()
        upload(sftp, PS_BIN, "/tmp/seeinps.new")
        sftp.close()
        show("上传 seeinps.new", *sudo_run(ssh, "cp /tmp/seeinps.new /root/seeinp/seeinps.new && chmod +x /root/seeinp/seeinps.new && cp /root/seeinp/seeinps /root/seeinp/seeinps.bak.0801 && ls -la /root/seeinp/ | grep -E 'seeinps'"))
        # 改 conf: username=user1 → user3, auth_code → 新码
        sudo_run(ssh, "sed -i 's/^username = .*/username = \"user3\"/' /root/seeinp/conf/seeinps.toml && sed -i 's/^auth_code = .*/auth_code = \"%s\"/' /root/seeinp/conf/seeinps.toml" % code)
        show("conf 修改结果", *sudo_run(ssh, "grep -A2 '\\[auth\\]' /root/seeinp/conf/seeinps.toml | sed 's/seeinp_.*/seeinp_(新码已写入)/'"))
    finally:
        ssh.close()

    # ===== 阶段2: PM 上传 + 改库 (不重启) =====
    print("========== 阶段2: PM 上传二进制/前端 + 更新 user3 授权码 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        sftp = ssh.open_sftp()
        upload(sftp, PM_BIN, "/root/seeinp/seeinpm.new")
        upload_dir(sftp, WEB_PM_DIST, "/root/seeinp/web-pm/dist.new")
        sftp.close()
        show("上传完成", *run(ssh, "ls -la /root/seeinp/seeinpm.new /root/seeinp/web-pm/dist.new/ | head -n 8"))
        show("备份并替换二进制", *run(ssh, "chmod +x /root/seeinp/seeinpm.new && cp /root/seeinp/seeinpm /root/seeinp/seeinpm.bak.0801 && echo backup_ok"))
        show("备份并替换前端", *run(ssh, "rm -rf /root/seeinp/web-pm/dist.bak && mv /root/seeinp/web-pm/dist /root/seeinp/web-pm/dist.bak && mv /root/seeinp/web-pm/dist.new /root/seeinp/web-pm/dist && ls /root/seeinp/web-pm/dist/"))
        # 更新 user3 授权码 (PM 仍在运行, sqlite 写入后注册时即时生效)
        sql = "UPDATE users SET auth_code='%s', auth_code_salt='%s', updated_at=strftime('%%s','now') WHERE username='user3'; SELECT username, substr(auth_code,1,16), status FROM users;" % (code_hash, salt)
        show("更新 user3 授权码", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"%s\"" % sql))
    finally:
        ssh.close()

    # ===== 阶段3: PS 重启 (先连旧 PM 验证新码可用) =====
    print("\n========== 阶段3: PS 重启 (新码连旧 PM) ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        show("停止旧 seeinps", *sudo_run(ssh, "pkill -f './seeinps' ; sleep 1 ; echo stopped"))
        sudo_run(ssh, "mv /root/seeinp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps")
        sudo_fire(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown")
        print("PS 已启动")
    finally:
        ssh.close()
    time.sleep(8)
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        show("PS 注册与端口(期望 Registered OK + 20001/20002/20003)", *sudo_run(ssh, "grep -E 'Registered|ALLOC|error|Error' /root/seeinp/logs/seeinps.log | tail -n 8"))
    finally:
        ssh.close()

    # ===== 阶段4: PM 重启 (升级到新版) =====
    print("\n========== 阶段4: PM 重启升级 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        show("停止旧 seeinpm", *run(ssh, "pkill -f './seeinpm' ; sleep 1 ; echo stopped"))
        run(ssh, "mv /root/seeinp/seeinpm.new /root/seeinp/seeinpm && chmod +x /root/seeinp/seeinpm")
        fire(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 < /dev/null & disown")
        print("PM 已启动")
    finally:
        ssh.close()
    time.sleep(8)

    # ===== 阶段5: 验证 =====
    print("\n========== 阶段5: 部署验证 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        show("PM 健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '无响应!'"))
        show("PM 监听端口(期望 999/9998/20001/20002/20003)", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
        show("PM 日志尾部(期望 Registered)", *run(ssh, "tail -n 12 /root/seeinp/logs/seeinpm.log"))
        show("ops代理存活(期望407,密码已被用户修改无法测认证)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
        show("TCP代理20001连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20001' && echo TCP_OK || echo TCP_FAIL"))
        show("TCP代理20002连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20002' && echo TCP_OK || echo TCP_FAIL"))
    finally:
        ssh.close()

    print("\n[完成] user3 新授权码: %s" % code)


if __name__ == "__main__":
    main()
