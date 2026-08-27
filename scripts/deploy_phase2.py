# -*- coding: utf-8 -*-
"""阶段2: PS数据修正(新密码+删测试代理) + PM清理20004残留 + 按序重启双端 + 端到端测试"""
import time

import paramiko

PS_HOST, PS_PORT, PS_USER, PS_PWD = "see.timemsee.cn", 27141, "hik", "Zbx.9705"
PM_HOST, PM_PORT, PM_USER, PM_PWD = "170.106.109.105", 22, "root", "Zbx.9705"
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


def fire(ssh, cmd):
    """发射后不管: nohup 启动类命令会挂起通道, 不读结果直接关闭"""
    chan = ssh.get_transport().open_session()
    chan.exec_command(cmd)
    time.sleep(2)
    chan.close()


def sudo_fire(ssh, cmd):
    """sudo 版发射后不管: 写入密码后不等结果直接关闭"""
    chan = ssh.get_transport().open_session()
    chan.exec_command("sudo -S sh -c " + "'" + cmd.replace("'", "'\\''") + "'")
    time.sleep(0.5)
    chan.sendall((PS_PWD + "\n").encode())
    time.sleep(2)
    chan.close()


def show(name, out, err=""):
    print(f"\n--- {name} ---")
    print((out or err).strip()[:1800])


# 1. PS 数据修正(进程未重启前改库, 重启后生效)
print("========== 1. PS 数据修正 ==========")
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
    show("确认代理数据", *sudo_run(ssh, "sqlite3 /root/seeinp/data/seeinps.db 'SELECT proxy_id, type, proxy_username, substr(proxy_password,1,7), public_port FROM local_proxies;'"))
finally:
    ssh.close()

# 2. PM 清理 e2e-verify 的端口分配残留 + 重启 PM(清除僵尸监听)
print("\n========== 2. PM 清理残留并重启 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("当前监听", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
    show("删除e2e-verify分配行", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db \"DELETE FROM port_allocations WHERE proxy_id='e2e-verify'; SELECT port, proxy_id, status FROM port_allocations ORDER BY port;\""))
    show("停止 seeinpm", *run(ssh, "pkill -f './seeinpm'; sleep 1; echo stopped"))
    fire(ssh, "cd /root/seeinp && nohup ./seeinpm -conf conf/seeinpm.toml > logs/seeinpm.log 2>&1 < /dev/null & disown")
    print("PM 已启动(发射后不管)")
finally:
    ssh.close()

time.sleep(3)

# 3. 重启 PS(加载新数据) — 先杀再启, PM 已就绪
print("\n========== 3. 重启 seeinps ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("停止 seeinps", *sudo_run(ssh, "pkill -f './seeinps'; sleep 1; echo stopped"))
    sudo_fire(ssh, "cd /root/seeinp && nohup ./seeinps -conf conf/seeinps.toml > logs/seeinps.log 2>&1 < /dev/null & disown")
    print("PS 已启动(发射后不管)")
finally:
    ssh.close()

time.sleep(10)

# 4. 验证注册与端口
print("\n========== 4. 验证注册与端口 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("PS进程", *run(ssh, "ps aux | grep seeinps | grep -v grep || echo '未运行!'"))
    show("PS日志(期望 Registered OK + 3个ALLOC)", *sudo_run(ssh, "grep -E 'ALLOC|Registered|Connecting' /root/seeinp/logs/seeinps.log | tail -n 8"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("PM健康", *run(ssh, "curl -s -m 5 http://127.0.0.1:9998/health || echo '无响应'"))
    show("PM端口监听(期望 999/9998/20001/20002/20003)", *run(ssh, "ss -tlnp | grep seeinpm | awk '{print $4}'"))
    show("PM分配记录", *run(ssh, "sqlite3 /root/seeinp/data/seeinpm.db 'SELECT port, proxy_id, proxy_type, status FROM port_allocations ORDER BY port;'"))
finally:
    ssh.close()

# 5. 运维代理端到端测试
print("\n========== 5. 运维代理端到端测试 ==========")
ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PM_HOST, port=PM_PORT, username=PM_USER, password=PM_PWD, timeout=10)
try:
    show("①带认证访问内网192.168.0.70(期望200)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("②无认证(期望407)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("③带认证访问公网(期望403 ACL拦截)", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://www.baidu.com/"))
    show("④经公网IP走代理(期望200)", *run(ssh, "curl -s -m 15 -x http://170.106.109.105:20003 -u 'test:Test@seeinp2026' -o /dev/null -w 'HTTP %{http_code}\\n' http://192.168.0.70/"))
    show("⑤页面内容抽样", *run(ssh, "curl -s -m 15 -x http://127.0.0.1:20003 -u 'test:Test@seeinp2026' http://192.168.0.70/ | head -c 300"))
finally:
    ssh.close()

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(PS_HOST, port=PS_PORT, username=PS_USER, password=PS_PWD, timeout=10)
try:
    show("⑥PS侧OPS访问日志(不含凭据)", *sudo_run(ssh, "grep '\\[OPS\\]' /root/seeinp/logs/seeinps.log | tail -n 6"))
finally:
    ssh.close()

print("\n阶段2完成")
