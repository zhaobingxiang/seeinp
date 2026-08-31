# -*- coding: utf-8 -*-
"""
本地 e2e：验证仪表盘改造所需 API
- PM /api/v1/proxies 返回 name/type/status/online/port/bytesIn/bytesOut/rateIn/rateOut
- PM /api/v1/audit-logs 返回最近动态所需字段
- PM /api/v1/port-pool 返回 ranges（端口池文案）
- PS /api/v1/status 返回 serverAddr/lastPing 新字段
- PS /api/v1/proxies 返回 id/type/localAddr/localPort/forwardPort
"""
import os
import re
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
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
    auth_code = r.json().get('data', {}).get('authCode', '')
    print('  create user1 authCode:', auth_code)

    conf_path = os.path.join(PS_DIR, 'conf', 'seeinps.toml')
    with open(conf_path, 'r', encoding='utf-8') as f:
        conf = f.read()
    conf = re.sub(r'auth_code = ".*"', f'auth_code = "{auth_code}"', conf)
    with open(conf_path, 'w', encoding='utf-8') as f:
        f.write(conf)

    print('=== 2. 启动 PS ===')
    start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
    if not wait_http(PSB + '/health', timeout=25):
        print('PS failed to start'); sys.exit(1)
    time.sleep(3)
    r = requests.get(f'{BASE}/api/v1/clients', headers=h, timeout=5)
    print('  clients:', r.text[:200])

    print('=== 3. PS 初始化 + 登录 ===')
    r = requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': auth_code, 'password': 'PsAdmin123'}, timeout=5)
    print('  PS init:', r.status_code, r.text[:120])
    time.sleep(1)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    pstoken = r.json().get('data', {}).get('token', '')
    ph = {'Authorization': f'Bearer {pstoken}'}
    print('  PS login ok')

    print('=== 4. PS 创建 2 个代理（TCP + 运维HTTP）===')
    r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'proxy1', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 8088}, headers=ph, timeout=5)
    print('  create proxy1:', r.status_code, r.text[:150])
    r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'ops1', 'type': 'ops_http', 'proxyUsername': 'ops-01', 'proxyPassword': 'Aa12345678!'}, headers=ph, timeout=5)
    print('  create ops1:', r.status_code, r.text[:150])
    time.sleep(3)

    print('=== 5. PS /api/v1/status 新字段验证 ===')
    r = requests.get(f'{PSB}/api/v1/status', headers=ph, timeout=5)
    data = r.json().get('data', {})
    print('  status:', r.text[:300])
    assert data.get('status') == 'connected', 'status != connected'
    assert 'serverAddr' in data, 'serverAddr missing'
    assert data.get('lastPing', 0) > 0, 'lastPing <= 0'
    print('  [OK] serverAddr=%s lastPing=%s' % (data['serverAddr'], data['lastPing']))

    print('=== 6. PS /api/v1/proxies 字段验证 ===')
    r = requests.get(f'{PSB}/api/v1/proxies', headers=ph, timeout=5)
    plist = r.json().get('data', [])
    print('  proxies:', r.text[:400])
    for p in plist:
        for k in ['id', 'type', 'localAddr', 'localPort', 'forwardPort']:
            assert k in p, f'proxy missing {k}'
    print('  [OK] %d proxies, fields complete' % len(plist))

    print('=== 7. PM /api/v1/proxies 仪表盘字段验证 ===')
    r = requests.get(f'{BASE}/api/v1/proxies', headers=h, timeout=5)
    plist = r.json().get('data', [])
    print('  pm proxies:', r.text[:400])
    for p in plist:
        for k in ['name', 'type', 'status', 'online', 'port', 'bytesIn', 'bytesOut', 'rateIn', 'rateOut']:
            assert k in p, f'pm proxy missing {k}'
    online = [p for p in plist if p.get('online')]
    print('  [OK] %d proxies, %d online, fields complete' % (len(plist), len(online)))

    print('=== 8. PM /api/v1/audit-logs 最近动态字段验证 ===')
    r = requests.get(f'{BASE}/api/v1/audit-logs?page_size=6', headers=h, timeout=5)
    body = r.json().get('data', {})
    items = body.get('list', [])
    print('  audit:', r.text[:400])
    for it in items:
        for k in ['username', 'action', 'target', 'createdAt']:
            assert k in it, f'audit missing {k}'
    print('  [OK] %d audit items, fields complete' % len(items))

    print('=== 9. PM /api/v1/port-pool 端口池文案验证 ===')
    r = requests.get(f'{BASE}/api/v1/port-pool', headers=h, timeout=5)
    body = r.json().get('data', {})
    print('  port-pool:', r.text[:300])
    assert 'ranges' in body and body['ranges'], 'ranges missing'
    print('  [OK] ranges=%s total=%s used=%s' % (body['ranges'], body.get('total'), body.get('used')))

    print()
    print('=== ALL DASHBOARD E2E CHECKS PASSED ===')


if __name__ == '__main__':
    try:
        main()
    finally:
        for p in procs:
            try:
                p.terminate()
            except Exception:
                pass
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
