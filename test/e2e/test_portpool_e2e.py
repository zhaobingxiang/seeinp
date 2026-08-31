# -*- coding: utf-8 -*-
"""
端口池配置 e2e：
1. 启动 PM/PS，建用户+代理（端口 20000）
2. GET port-pool 验证 ranges/allocations
3. PUT 改小（不含现有端口）action=recycle → 代理重连拿到池内新端口
4. PUT action=keep → 代理保留池外端口
5. 非法范围 400、重叠自动合并
6. 重启 PM 后配置持久化（DB）
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
            requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def main():
    print('=== 0. 启动 ===')
    cleanup()
    for exe in ['seeinpm.exe', 'seeinps.exe']:
        subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
    time.sleep(1)
    start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
    wait_http(BASE + '/health')
    r = requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    token = r.json().get('data', {}).get('access_token', '')
    h = {'Authorization': f'Bearer {token}'}
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
    auth_code = r.json().get('data', {}).get('authCode', '')
    conf_path = os.path.join(PS_DIR, 'conf', 'seeinps.toml')
    with open(conf_path, 'r', encoding='utf-8') as f:
        conf = f.read()
    conf = re.sub(r'auth_code = ".*"', f'auth_code = "{auth_code}"', conf)
    with open(conf_path, 'w', encoding='utf-8') as f:
        f.write(conf)
    start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
    wait_http(PSB + '/health', timeout=25)
    time.sleep(3)
    requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': auth_code, 'password': 'PsAdmin123'}, timeout=5)
    time.sleep(1)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    ph = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}
    r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'e2eproxy', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 8088}, headers=ph, timeout=5)
    print('  create proxy ->', r.json().get('data', {}).get('forwardPort'))

    print('=== 1. GET port-pool ===')
    r = requests.get(f'{BASE}/api/v1/port-pool', headers=h, timeout=5)
    print(' ', r.text[:400])

    print('=== 2. PUT 改小为 21000-21100 + recycle（20000 应被回收，代理重连拿 21000）===')
    r = requests.put(f'{BASE}/api/v1/port-pool', json={'ranges': [{'start': 21000, 'end': 21100}], 'action': 'recycle'}, headers=h, timeout=5)
    print(' ', r.text[:400])
    time.sleep(8)  # 等 seeinps 重连 + ALLOC
    r = requests.get(f'{PSB}/api/v1/proxies', headers=ph, timeout=5)
    ports = [p.get('forwardPort') for p in r.json().get('data', [])]
    print('  proxy ports after recycle:', ports)
    assert ports == [21000], f'expected port 21000 in pool, got {ports}'
    print('  OK: recycled to pool port')

    print('=== 3. 再改小为 22000-22050 + keep（代理应保留 21000 不被踢）===')
    r = requests.put(f'{BASE}/api/v1/port-pool', json={'ranges': [{'start': 22000, 'end': 22050}], 'action': 'keep'}, headers=h, timeout=5)
    print(' ', r.text[:300])
    time.sleep(3)
    r = requests.get(f'{PSB}/api/v1/proxies', headers=ph, timeout=5)
    ports = [p.get('forwardPort') for p in r.json().get('data', [])]
    print('  proxy ports after keep:', ports)
    assert ports == [21000], f'keep should preserve 21000, got {ports}'
    print('  OK: keep preserved port')

    print('=== 4. 非法范围 400 ===')
    r = requests.put(f'{BASE}/api/v1/port-pool', json={'ranges': [{'start': 500, 'end': 100}]}, headers=h, timeout=5)
    print('  invalid range ->', r.status_code, r.text[:120])
    r = requests.put(f'{BASE}/api/v1/port-pool', json={'ranges': []}, headers=h, timeout=5)
    print('  empty ranges ->', r.status_code, r.text[:120])

    print('=== 5. 重叠自动合并 ===')
    r = requests.put(f'{BASE}/api/v1/port-pool', json={'ranges': [{'start': 30000, 'end': 30050}, {'start': 30040, 'end': 30100}, {'start': 40000, 'end': 40010}], 'action': 'keep'}, headers=h, timeout=5)
    print(' ', r.text[:300])
    assert '"start":30000' in r.text and '"end":30100' in r.text, 'expected merged 30000-30100'

    print('=== 6. 重启 PM 验证 DB 持久化 ===')
    procs[0].terminate()
    time.sleep(2)
    start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
    wait_http(BASE + '/health', timeout=20)
    time.sleep(2)
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    h = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('access_token', '')}
    r = requests.get(f'{BASE}/api/v1/port-pool', headers=h, timeout=5)
    print('  after restart:', r.text[:300])
    assert '30000' in r.text and '40010' in r.text, 'config should persist in DB after restart'
    print('  OK: persisted in DB')

    print('=== DONE ===')


if __name__ == '__main__':
    try:
        main()
    finally:
        for p in procs:
            p.terminate()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
