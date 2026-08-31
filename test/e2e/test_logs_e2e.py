# -*- coding: utf-8 -*-
"""
本地 e2e：验证日志混合架构 + 日志增强
- PS 审计落库本地 + AUDIT_SYNC 上报 PM（source='ps'）
- PM 审计支持 source/keyword/start_time/end_time 筛选
- PM 经控制通道拉取 PS 运行日志（ps-logs 列表 + content）
- 运行日志行首时间戳
- download=1 下载
"""
import os
import shutil
import subprocess
import sys
import time

import requests

BASE = 'http://127.0.0.1:9998'
PSB = 'http://127.0.0.1:65443'
RT = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'runtime')
PM_DIR = os.path.join(RT, 'pm')
PS_DIR = os.path.join(RT, 'ps')

procs = []


def cleanup():
    ts = time.strftime('%H%M%S')
    for d in [os.path.join(PM_DIR, 'data'), os.path.join(PS_DIR, 'data')]:
        for f in os.listdir(d):
            p = os.path.join(d, f)
            if f.endswith('.db') or f.endswith('.db-wal') or f.endswith('.db-shm'):
                try:
                    os.rename(p, p + f'.bak{ts}')
                except OSError:
                    pass
    for d in [os.path.join(PM_DIR, 'logs'), os.path.join(PS_DIR, 'logs')]:
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d, exist_ok=True)


def start(workdir, exe):
    logpath = os.path.join(workdir, 'logs', 'seeinpm.log' if 'pm' in workdir else 'seeinps.log')
    f = open(logpath, 'wb', buffering=0)
    p = subprocess.Popen([exe, '-conf', 'conf/seeinpm.toml' if 'pm' in workdir else 'conf/seeinps.toml'],
                         cwd=workdir, stdout=f, stderr=subprocess.STDOUT)
    procs.append(p)
    return p


def wait_http(url, timeout=20):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            r = requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def main():
    print('=== 0. 清理并启动 PM/PS ===')
    cleanup()
    # 杀残留进程
    for exe in ['seeinpm.exe', 'seeinps.exe']:
        subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
    time.sleep(1)
    start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
    if not wait_http(BASE + '/health'):
        print('PM failed to start'); sys.exit(1)
    print('  PM up')

    print('=== 1. PM 初始化 + 登录 + 建用户 ===')
    r = requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    print('  init:', r.status_code, r.text[:100])
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    token = r.json().get('data', {}).get('access_token', '')
    h = {'Authorization': f'Bearer {token}'}
    print('  login token:', token[:20], '...')
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
    auth_code = r.json().get('data', {}).get('authCode', '')
    print('  create user1 authCode:', auth_code)

    # 更新 PS 配置的授权码
    conf_path = os.path.join(PS_DIR, 'conf', 'seeinps.toml')
    with open(conf_path, 'r', encoding='utf-8') as f:
        conf = f.read()
    import re
    conf = re.sub(r'auth_code = ".*"', f'auth_code = "{auth_code}"', conf)
    with open(conf_path, 'w', encoding='utf-8') as f:
        f.write(conf)
    print('  seeinps.toml auth_code updated')

    print('=== 2. 启动 PS ===')
    start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
    if not wait_http(PSB + '/health', timeout=25):
        print('PS failed to start'); sys.exit(1)
    print('  PS up, waiting register...')
    time.sleep(3)
    r = requests.get(f'{BASE}/api/v1/clients', headers=h, timeout=5)
    print('  clients:', r.text[:200])

    print('=== 3. PS 初始化 + 登录（触发本地审计 + 上报）===')
    r = requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': auth_code, 'password': 'PsAdmin123'}, timeout=5)
    print('  PS init:', r.status_code, r.text[:120])
    time.sleep(1)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    pstoken = r.json().get('data', {}).get('token', '')
    ph = {'Authorization': f'Bearer {pstoken}'}
    print('  PS login token:', pstoken[:20], '...')

    print('=== 4. PS 创建代理（触发 proxy_create 审计）===')
    r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'e2eproxy', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 8088}, headers=ph, timeout=5)
    print('  PS create proxy:', r.status_code, r.text[:150])

    print('=== 5. 等 2s，验证 PS 本地审计 ===')
    time.sleep(2)
    r = requests.get(f'{PSB}/api/v1/audit-logs?page_size=10', headers=ph, timeout=5)
    print('  PS audit:', r.text[:400])

    print('=== 6. 验证 PM 审计（应含 source=ps 的上报记录）===')
    time.sleep(2)
    r = requests.get(f'{BASE}/api/v1/audit-logs?page_size=20', headers=h, timeout=5)
    print('  PM audit all:', r.text[:600])
    r = requests.get(f'{BASE}/api/v1/audit-logs?source=ps&page_size=10', headers=h, timeout=5)
    print('  PM audit source=ps:', r.text[:400])

    print('=== 7. 验证 PM 审计关键词/时间筛选 ===')
    r = requests.get(f'{BASE}/api/v1/audit-logs?keyword=proxy_create&page_size=10', headers=h, timeout=5)
    print('  keyword=proxy_create:', r.text[:300])
    r = requests.get(f'{BASE}/api/v1/audit-logs?source=ps&keyword=login&page_size=10', headers=h, timeout=5)
    print('  source=ps keyword=login:', r.text[:300])
    now = int(time.time())
    r = requests.get(f'{BASE}/api/v1/audit-logs?start_time={now - 60}&end_time={now + 60}&page_size=10', headers=h, timeout=5)
    print('  time range [now-60, now+60]:', r.text[:200])

    print('=== 8. 验证 PM 拉取 PS 运行日志（ps-logs）===')
    r = requests.get(f'{BASE}/api/v1/ps-logs?username=user1', headers=h, timeout=15)
    print('  ps-logs:', r.text[:300])
    r = requests.get(f'{BASE}/api/v1/ps-logs/content?username=user1&file=seeinps.log&lines=100', headers=h, timeout=15)
    print('  ps-logs/content:', r.text[:400])

    print('=== 9. 验证运行日志时间戳（PS 本地文件）===')
    logf = os.path.join(PS_DIR, 'logs', 'seeinps.log')
    if os.path.exists(logf):
        with open(logf, 'r', encoding='utf-8', errors='replace') as f:
            lines = f.read().split('\n')[:8]
        for ln in lines:
            if ln.strip():
                print('  |', ln[:100])
    else:
        print('  NO seeinps.log')

    print('=== 10. 验证 download=1（PM 本机日志）===')
    r = requests.get(f'{BASE}/api/v1/logs/content?file=seeinpm.log&download=1', headers=h, timeout=10)
    print('  download status:', r.status_code, 'ct:', r.headers.get('Content-Type'), 'len:', len(r.content))
    print('  disposition:', r.headers.get('Content-Disposition'))

    print('=== 11. 验证 PS 审计 keyword 筛选 ===')
    r = requests.get(f'{PSB}/api/v1/audit-logs?keyword=proxy&page_size=10', headers=ph, timeout=5)
    print('  PS keyword=proxy:', r.text[:300])

    print('=== DONE ===')


if __name__ == '__main__':
    try:
        main()
    finally:
        for p in procs:
            p.terminate()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
