# -*- coding: utf-8 -*-
"""部署 seeinpm 注册死锁修复 + 重置 test 代理密码 + 清理测试数据 + 端到端验证运维HTTP代理。

修复内容(seeinpm):
- handleRegister 不再盲信 DB 的 online_session 残留值, 改为校验内存活跃会话(心跳新鲜+mux未关闭)
- 被顶掉的旧连接延迟清理时按 sessionID 条件清除, 不误删新会话状态

数据操作(seeinps.db):
- http 代理(用户名 test)密码重置为 Test@seeinp2026 (bcrypt)
- 删除 e2e-verify 测试残留代理
"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
LOCAL_PM_BIN = r"d:\ide\seeinp\seeinpm-linux-amd64"
BCRYPT = "$2a$10$dY1dYgjF/.JOYLTvkz5SieXELF80QFdGY3WqjTIW9jl3baQhiQEM6"


def sudo_run(ssh, cmd, timeout=25):
    stdin, stdout, stderr = ssh.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'", timeout=timeout)
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
    print((out or err).strip()[:1800])


# ========== 阶段1: 部署 seeinpm 修复 ==========
print("========== 阶段1: 部署 seeinpm ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("停止 seeinpm", *run(ssh, "pkill -f './seeinpm'; sleep 1; echo stopped"))
    sftp = ssh.open_sftp()
    sftp.put(LOCAL_PM_BIN, "/tmp/seeinpm.new")
    sftp.close()
    print("二进制上传完成")
    show("替换二进制", *run(ssh, "mv /tmp/seeinpm.new /root/seeinp/seeinpm && chmod +x /root/seeinp/seeinpm && md5sum /root/seeinp/seeinpm"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("启动 seeinpm", *run(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 < /dev/null & disown; echo launched"))
finally:
    ssh.close()

time.sleep(3)
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '无响应'"))
finally:
    ssh.close()

# ========== 阶段2: seeinps 数据修正 + 重启 ==========
print("\n========== 阶段2: seeinps 数据修正 + 重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    sql = ("UPDATE local_proxies SET proxy_password='" + BCRYPT + "' WHERE proxy_id='http';\n"
           "DELETE FROM local_proxies WHERE proxy_id='e2e-verify';\n")
    sftp = ssh.open_sftp()
    with sftp.open("/tmp/fix_ops.sql", "wb") as f:
        f.write(sql.encode())
    sftp.close()
    show("执行SQL(重置密码+删测试代理)", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db < /tmp/fix_ops.sql && rm -f /tmp/fix_ops.sql && echo SQL_OK"))
    show("确认代理数据", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, proxy_username, substr(proxy_password,1,7), public_port, acl FROM local_proxies;'"))
    show("停止 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; echo stopped"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("启动 seeinps", *sudo_run(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown; echo launched"))
finally:
    ssh.close()

time.sleep(10)

# ========== 阶段3: 验证注册与端口恢复 ==========
print("\n========== 阶段3: 验证注册与端口 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("PS进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("PS健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:65443/health || echo '无响应'"))
    show("PS日志(期望 Registered OK + 20001/20002/20003)", *sudo_run(ssh, "grep -E 'ALLOC|Registered|Replacing|Connecting' /root/seeinp/logs/seeinps.log | tail -n 8"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM端口监听(期望 999/9998/20001/20002/20003)", *run(ssh, "ss -tlnp | grep seeinpm"))
    show("PM分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, proxy_type, status FROM port_allocations ORDER BY port;'"))
    show("PM日志尾部", *run(ssh, "tail -n 12 /root/seeinp/logs/seeinpm.log"))
finally:
    ssh.close()

# ========== 阶段4: 运维HTTP代理端到端测试(从公网服务器) ==========
print("\n========== 阶段4: 运维代理端到端测试 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("①带认证访问内网192.168.0.70(期望200)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("②无认证(期望407)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("③带认证访问公网(期望403 ACL拦截)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://www.baidu.com/"))
    show("④经公网IP走代理(期望200)", *run(ssh, "curl -s -m 15 -x http://170.106.109.105:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("⑤页面内容抽样", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' http://192.168.0.70/ | head -c 300"))
    show("⑥PS侧OPS访问日志", *run(ssh, "echo '(见PS日志)'"))
finally:
    ssh.close()

# PS 侧查看 ops 访问日志
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("⑥OPS访问日志(不含凭据)", *sudo_run(ssh, "grep '\\[OPS\\]' /root/seeinp/logs/seeinps.log | tail -n 6"))
finally:
    ssh.close()

print("\n部署与测试完成")
