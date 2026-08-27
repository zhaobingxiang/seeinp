# -*- coding: utf-8 -*-
"""部署重绑(rebind)版 seeinps + web-pm 到测试服务器:
- 授权码重置后进程不退出, B端横幅提示 + /api/v1/auth/rebind 恢复
- 本次部署同步 seeinps 新前端提示文案与 web-pm 授权码复制按钮修复
- 部署顺序: 停进程 → 备份 → 拷贝(seeinps二进制+web-ps) → 部署 web-pm dist → md5校验 → 启动/验证
"""
import os
import sys
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
PS_BIN = r"d:\ide\seeinp\dist\release\seeinps_linux\seeinps"
WEB_PS_DIST = r"d:\ide\seeinp\web-ps\dist"
WEB_PM_DIST = r"d:\ide\seeinp\web-pm\dist"
BAK_TAG = "0827rebind"


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
    print(text[:1200] if text else "(empty)")
    print()


def upload_dir(sftp, local_dir, remote_dir):
    try:
        sftp.stat(remote_dir)
    except FileNotFoundError:
        sftp.mkdir(remote_dir)
    for name in os.listdir(local_dir):
        lp = os.path.join(local_dir, name)
        rp = remote_dir + "/" + name
        if os.path.isdir(lp):
            upload_dir(sftp, lp, rp)
        else:
            sftp.put(lp, rp)


def main():
    # ===== 阶段1: 上传二进制 + 前端 =====
    print("========== 阶段1: 上传二进制与前端到 PS 服务器 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        sftp = ssh.open_sftp()
        sftp.put(PS_BIN, "/tmp/seeinps.new")
        sudo_run(ssh, "rm -rf /tmp/webnew")
        upload_dir(sftp, WEB_PS_DIST, "/tmp/webnew")
        sftp.close()
        show("上传完成", *sudo_run(ssh, "md5sum /tmp/seeinps.new && ls /tmp/webnew/"))
    finally:
        ssh.close()

    # ===== 阶段2: 停进程 → 备份 → 替换 → md5 → 启动 =====
    print("========== 阶段2: 停止/备份/替换/启动 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        show("停进程", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; ps aux | grep seeinps | grep -v grep || echo '已停止'"))
        show("备份二进制与web", *sudo_run(
            ssh,
            "cp /root/seeinp/seeinps /root/seeinp/seeinps.bak.%s && "
            "rm -rf /root/seeinp/web.bak.%s && cp -r /root/seeinp/web /root/seeinp/web.bak.%s && echo BACKUP_OK" % (BAK_TAG, BAK_TAG, BAK_TAG)))
        show("替换二进制", *sudo_run(ssh, "cp /tmp/seeinps.new /root/seeinp/seeinps && chmod +x /root/seeinp/seeinps && echo COPY_OK"))
        show("替换前端", *sudo_run(ssh, "rm -rf /root/seeinp/web && mkdir -p /root/seeinp/web && cp -r /tmp/webnew/* /root/seeinp/web/ && ls /root/seeinp/web/"))
        show("md5 一致性", *sudo_run(ssh, "md5sum /tmp/seeinps.new /root/seeinp/seeinps | awk '{print $1}' | uniq -c"))
        sudo_fire(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown")
        print("PS 已启动")
    finally:
        ssh.close()

    # ===== 阶段3: 部署 web-pm 修复（授权码复制按钮 + 前端重建） =====
    print("========== 阶段3: 部署 web-pm 修复 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        sftp = ssh.open_sftp()
        upload_dir(sftp, WEB_PM_DIST, "/tmp/webpmnew.upload")
        sftp.close()
        run(ssh, "rm -rf /tmp/webpmnew && mv /tmp/webpmnew.upload /tmp/webpmnew")
        show("上传 web-pm", *run(ssh, "ls /tmp/webpmnew/"))
        run(ssh, "mkdir -p /root/seeinp/web-pm/dist.bak && cp -a /root/seeinp/web-pm/dist/. /root/seeinp/web-pm/dist.bak/ || true")
        show("替换 web-pm", *run(ssh, "rm -rf /root/seeinp/web-pm/dist && mkdir -p /root/seeinp/web-pm/dist && cp -r /tmp/webpmnew/* /root/seeinp/web-pm/dist/ && ls /root/seeinp/web-pm/dist/"))
        show("PM 无重启说明", *run(ssh, "echo 'web-pm is static assets, no restart required'"))
    finally:
        ssh.close()

    time.sleep(10)

    # ===== 阶段4: PS 侧验证 =====
    print("========== 阶段4: PS 侧验证 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
    try:
        show("进程存活", *sudo_run(ssh, "ps aux | grep seeinps | grep -v grep || echo '进程不存在!'"))
        show("日志(期望 Registered OK + 3 ALLOC)", *sudo_run(ssh, "grep -E 'Registered|ALLOC|error|Error|CTRL' /root/seeinp/logs/seeinps.log | tail -n 8"))
        show("B端 65443 监听", *sudo_run(ssh, "ss -tlnp | grep 65443 || echo '未监听!'"))
        show("B端 web 可访问", *sudo_run(ssh, "curl -s -m 5 -o /dev/null -w 'HTTP %{http_code}\\n' http://127.0.0.1:65443/"))
        show("B端 auth/status(期望 initialized=true authCodeReset=false)", *sudo_run(ssh, "curl -s -m 5 http://127.0.0.1:65443/api/v1/auth/status"))
        show("rebind 接口存在(未登录期望 401)", *sudo_run(ssh, "curl -s -m 5 -o /dev/null -w 'HTTP %{http_code}\\n' -X POST http://127.0.0.1:65443/api/v1/auth/rebind"))
        show("B端 web 资产(期望新前端)", *sudo_run(ssh, "grep -R \"绑定请求已发送\" /root/seeinp/web/assets/ | head -n 1 || echo '未找到提示文案'"))
        show("本地代理数据保留", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db \"SELECT proxy_id, type, public_port FROM local_proxies;\""))
    finally:
        ssh.close()

    # ===== 阶段5: PM 侧验证 =====
    print("========== 阶段5: PM 侧验证 ==========")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
    try:
        show("PM 健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health"))
        show("PM 监听(期望 999/9998/20001/20002/20003)", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
        show("user3 在线状态", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"SELECT username, status, online_session IS NOT NULL FROM users;\""))
        show("TCP 20001 连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20001' && echo TCP_OK || echo TCP_FAIL"))
        show("TCP 20002 连通", *run(ssh, "timeout 3 bash -c '</dev/tcp/127.0.0.1/20002' && echo TCP_OK || echo TCP_FAIL"))
        show("ops 20003 存活(期望407)", *run(ssh, "curl -s -m 10 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    finally:
        ssh.close()

    print("\n[完成] 部署结束")


if __name__ == "__main__":
    main()
